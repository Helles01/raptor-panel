package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/nodes"
	"go.yaml.in/yaml/v3"
)

type rotationStore struct {
	nodes       map[uint64]nodes.WingsNode
	replaced    map[uint64]nodes.Credentials
	replaceRuns int
	failRead    error
	failReplace error
}

func (s *rotationStore) NodeConfig(_ context.Context, id uint64) (nodes.WingsNode, error) {
	if s.failRead != nil {
		return nodes.WingsNode{}, s.failRead
	}
	n, ok := s.nodes[id]
	if !ok {
		return nodes.WingsNode{}, ErrNotFound
	}
	return n, nil
}

func (s *rotationStore) ReplaceNodeToken(_ context.Context, id uint64, creds nodes.Credentials) error {
	s.replaceRuns++
	if s.failReplace != nil {
		return s.failReplace
	}
	if s.replaced == nil {
		s.replaced = map[uint64]nodes.Credentials{}
	}
	s.replaced[id] = creds
	return nil
}

func newRotation(t *testing.T, appURL string) (*Rotation, *rotationStore) {
	t.Helper()
	st := &rotationStore{nodes: map[uint64]nodes.WingsNode{
		4: {UUID: "6f1c0b1e-0000-4000-8000-000000000004", FQDN: "n1.example.com", Scheme: "https", DaemonBase: "/var/lib/pterodactyl/volumes", DaemonSFTP: 2022, DaemonListen: 8080, UploadSize: 100, AllowedMounts: []string{"/srv/shared"}},
	}}
	r, err := NewRotation(st, appURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r, st
}

func TestNewRotationNeedsAStoreAndAnAddress(t *testing.T) {
	if _, err := NewRotation(nil, "https://panel.example.com", nil); err == nil {
		t.Error("no store")
	}
	if _, err := NewRotation(&rotationStore{}, "", nil); err == nil {
		t.Error("no address")
	}
	if r, err := NewRotation(&rotationStore{}, "https://panel.example.com", nil); err != nil || r.random == nil {
		t.Errorf("default random: %v", err)
	}
}

func TestRotateNodeToken(t *testing.T) {
	r, st := newRotation(t, "https://panel.example.com")
	got, err := r.RotateNodeToken(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := st.replaced[4]
	if !ok || st.replaceRuns != 1 || len(stored.TokenID) != 16 || len(stored.Token) != 64 {
		t.Fatalf("stored = %+v (%d runs)", stored, st.replaceRuns)
	}
	if got.ID != 4 || got.UUID != st.nodes[4].UUID {
		t.Fatalf("result = %+v", got)
	}
	// The file is what Wings needs from now on: the identity, the token that was stored, the address of the panel, and the mounts.
	var file map[string]any
	if err := yaml.Unmarshal(got.Config, &file); err != nil {
		t.Fatal(err)
	}
	if file["uuid"] != got.UUID || file["token_id"] != stored.TokenID || file["token"] != stored.Token || file["remote"] != "https://panel.example.com" {
		t.Errorf("config = %v", file)
	}
	if mounts, _ := file["allowed_mounts"].([]any); len(mounts) != 1 || mounts[0] != "/srv/shared" {
		t.Errorf("allowed_mounts = %v", file["allowed_mounts"])
	}

	// A second turn gives yet another token.
	again, err := r.RotateNodeToken(context.Background(), 4)
	if err != nil || st.replaced[4] == stored || string(again.Config) == string(got.Config) {
		t.Errorf("the second turn repeated the first: %v", err)
	}
}

func TestRotateNodeTokenLeavesTheNodeAloneWhenItCannotFinish(t *testing.T) {
	boom := errors.New("the database is down")
	for name, tc := range map[string]struct {
		appURL string
		id     uint64
		setup  func(*rotationStore)
		want   error
	}{
		"a node that is not there":   {"https://panel.example.com", 9, func(*rotationStore) {}, ErrNotFound},
		"a node that cannot be read": {"https://panel.example.com", 4, func(s *rotationStore) { s.failRead = boom }, boom},
		// The file is made before the token is stored, so this node keeps the token it has.
		"an address of the panel that makes no file": {"not an address", 4, func(*rotationStore) {}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			r, st := newRotation(t, tc.appURL)
			tc.setup(st)
			got, err := r.RotateNodeToken(context.Background(), tc.id)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("err = %v", err)
			}
			if st.replaceRuns != 0 || got.Config != nil {
				t.Errorf("the node was changed (%d runs) or a file was given: %+v", st.replaceRuns, got)
			}
		})
	}
}

// When the store refuses, no file is given: it would hold a token that is not the node's.
func TestRotateNodeTokenGivesNoFileWhenTheStoreFails(t *testing.T) {
	r, st := newRotation(t, "https://panel.example.com")
	st.failReplace = errors.New("deadlock")
	got, err := r.RotateNodeToken(context.Background(), 4)
	if !errors.Is(err, st.failReplace) || got.Config != nil || got.ID != 0 {
		t.Errorf("got %+v, err = %v", got, err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("no randomness") }

func TestRotateNodeTokenNeedsRandomness(t *testing.T) {
	st := &rotationStore{nodes: map[uint64]nodes.WingsNode{4: {UUID: "u", FQDN: "n1.example.com", Scheme: "https", DaemonListen: 8080}}}
	r, err := NewRotation(st, "https://panel.example.com", brokenReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RotateNodeToken(context.Background(), 4); err == nil || st.replaceRuns != 0 || strings.Contains(err.Error(), "token\":") {
		t.Errorf("err = %v, runs %d", err, st.replaceRuns)
	}
}
