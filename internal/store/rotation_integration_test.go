//go:build integration

package store_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/Helles01/raptor-panel/internal/admin"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/auth"
	"go.yaml.in/yaml/v3"
)

func (r *rig) rotation() *admin.Rotation {
	r.t.Helper()
	rot, err := admin.NewRotation(r.db.AdminRotation(), "https://panel.example.com", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return rot
}

type nodeFile struct {
	UUID    string `yaml:"uuid"`
	TokenID string `yaml:"token_id"`
	Token   string `yaml:"token"`
}

func readNodeFile(t *testing.T, b []byte) nodeFile {
	t.Helper()
	var f nodeFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAdminRotateNodeToken(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	loc, err := svc.CreateLocation(r.ctx, "eu", "")
	if err != nil {
		t.Fatal(err)
	}
	node, err := svc.CreateNode(r.ctx, validNode(loc))
	if err != nil {
		t.Fatal(err)
	}
	before := readNodeFile(t, node.Config)
	if _, err := r.db.AuthzNodes().ByTokenID(r.ctx, before.TokenID); err != nil {
		t.Fatalf("the node does not authenticate before: %v", err)
	}

	rotated, err := r.rotation().RotateNodeToken(r.ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := readNodeFile(t, rotated.Config)
	if rotated.ID != node.ID || after.UUID != before.UUID || after.TokenID == before.TokenID || after.Token == before.Token {
		t.Fatalf("before %+v, after %+v", before, after)
	}
	// The old token is gone at once, and the file that was given is the one the authorizer checks now.
	if _, err := r.db.AuthzNodes().ByTokenID(r.ctx, before.TokenID); !errors.Is(err, authz.ErrNoNode) {
		t.Errorf("the old token still finds the node: %v", err)
	}
	got, err := r.db.AuthzNodes().ByTokenID(r.ctx, after.TokenID)
	if err != nil || got.Node.ID != node.ID || got.Token != after.Token {
		t.Fatalf("new credentials = %+v, %v", got, err)
	}
	// The sealed value was replaced, not added to, and the plain column was not touched.
	var sealed int
	var plain string
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'nodes' AND row_id = ? AND column_name = 'daemon_token'", node.ID).Scan(&sealed)
	r.sql.QueryRow("SELECT daemon_token FROM nodes WHERE id = ?", node.ID).Scan(&plain)
	if sealed != 1 || plain != "" {
		t.Errorf("%d sealed tokens, the column holds %q", sealed, plain)
	}

	for name, id := range map[string]uint64{"a node that is not there": 99, "id zero": 0, "an id too big for the column": 1 << 32} {
		if _, err := r.rotation().RotateNodeToken(r.ctx, id); !errors.Is(err, admin.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if again, err := r.db.AuthzNodes().ByTokenID(r.ctx, after.TokenID); err != nil || again.Token != after.Token {
		t.Errorf("a refused turn changed the node: %+v, %v", again, err)
	}
}

// A node that came with the database of the old panel has only the old, encrypted token and no sealed one. It can be rotated all the same:
// that is how its token, which counts as known to others, is replaced.
func TestAdminRotateTokenOfAMigratedNode(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	r.node(5, "oldtokenid00005")
	if _, err := r.db.AuthzNodes().ByTokenID(r.ctx, "oldtokenid00005"); !errors.Is(err, authz.ErrNoNode) {
		t.Fatalf("a migrated node authenticates before: %v", err)
	}
	rotated, err := r.rotation().RotateNodeToken(r.ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	f := readNodeFile(t, rotated.Config)
	got, err := r.db.AuthzNodes().ByTokenID(r.ctx, f.TokenID)
	if err != nil || got.Node.ID != 5 || got.Token != f.Token {
		t.Fatalf("credentials = %+v, %v", got, err)
	}
}

// Rotations that run together must not leave the token id of one and the token of another: the file the last one gave out has to be the node's.
func TestAdminRotateNodeTokenUnderLoad(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	r.node(5, "oldtokenid00005")
	rot := r.rotation()

	const runs = 8
	files := make([]nodeFile, runs)
	var wg sync.WaitGroup
	errs := make(chan error, runs)
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := rot.RotateNodeToken(r.ctx, 5)
			if err != nil {
				errs <- err
				return
			}
			files[i] = readNodeFile(t, got.Config)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var tokenID string
	r.sql.QueryRow("SELECT daemon_token_id FROM nodes WHERE id = 5").Scan(&tokenID)
	got, err := r.db.AuthzNodes().ByTokenID(r.ctx, tokenID)
	if err != nil {
		t.Fatalf("the node does not authenticate after: %v", err)
	}
	for _, f := range files {
		if f.TokenID == tokenID && f.Token == got.Token {
			return
		}
	}
	t.Errorf("no file that was given out matches the node: stored id %s", tokenID)
}

func TestAdminRevokeKeysInBulk(t *testing.T) {
	r := newRig(t)
	resetAdminTables(r)
	svc := r.service()
	for _, name := range []string{"ada", "bob"} {
		if _, err := svc.CreateUser(r.ctx, admin.UserInput{Email: name + "@example.com", Username: name, FirstName: "A", LastName: "B", Admin: name == "ada"}); err != nil {
			t.Fatal(err)
		}
	}
	made := map[string]admin.CreatedKey{}
	for name, in := range map[string]admin.KeyInput{
		"ada account":     {User: "ada", Description: "a"},
		"ada application": {User: "ada", Application: true, Description: "b", Access: []string{"servers=rw"}},
		"bob account":     {User: "bob", Description: "c"},
	} {
		k, err := svc.CreateKey(r.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		made[name] = k
	}
	// Secrets of other things must come through: a node's token and a user's second factor.
	loc, err := svc.CreateLocation(r.ctx, "eu", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateNode(r.ctx, validNode(loc)); err != nil {
		t.Fatal(err)
	}
	r.seal("users", 1, "totp_secret", "JBSWY3DPEHPK3PXP")

	count := func(q string) (n int) { r.sql.QueryRow(q).Scan(&n); return }
	keys := func() int { return count("SELECT COUNT(*) FROM api_keys") }
	sealedKeys := func() int { return count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'api_keys'") }
	if keys() != 3 || sealedKeys() != 3 {
		t.Fatalf("%d keys, %d sealed", keys(), sealedKeys())
	}

	dir, err := admin.NewDirectory(r.db.AdminDirectory())
	if err != nil {
		t.Fatal(err)
	}
	// Without the go-ahead nothing changes.
	var nc *admin.NotConfirmedError
	if _, err := dir.RevokeKeys(r.ctx, "", false); !errors.As(err, &nc) || nc.Count != 3 || keys() != 3 {
		t.Fatalf("not confirmed: %v, %d keys", err, keys())
	}

	// The keys of accounts go, with their sealed hashes; the application key stays and still works.
	if n, err := dir.RevokeKeys(r.ctx, "account", true); err != nil || n != 2 || keys() != 1 || sealedKeys() != 1 {
		t.Fatalf("accounts: n = %d, err = %v, %d keys, %d sealed", n, err, keys(), sealedKeys())
	}
	for _, name := range []string{"ada account", "bob account"} {
		ident, _, _ := auth.SplitKey(made[name].Token)
		if _, err := r.db.AuthzKeys().ByIdentifier(r.ctx, ident); !errors.Is(err, authz.ErrNoKey) {
			t.Errorf("%s still found: %v", name, err)
		}
	}
	ident, secret, _ := auth.SplitKey(made["ada application"].Token)
	if k, err := r.db.AuthzKeys().ByIdentifier(r.ctx, ident); err != nil || k.SecretHash != auth.HashKeySecret(secret) {
		t.Errorf("the application key was touched: %+v, %v", k, err)
	}

	if n, err := dir.RevokeKeys(r.ctx, "", true); err != nil || n != 1 || keys() != 0 || sealedKeys() != 0 {
		t.Fatalf("all: n = %d, err = %v, %d keys, %d sealed", n, err, keys(), sealedKeys())
	}
	if n, err := dir.RevokeKeys(r.ctx, "", true); err != nil || n != 0 {
		t.Errorf("nothing left: n = %d, err = %v", n, err)
	}

	if other := count("SELECT COUNT(*) FROM raptor_secrets WHERE table_name <> 'api_keys'"); other != 2 {
		t.Errorf("%d other sealed values are left, want the token of the node and the second factor", other)
	}
}
