//go:build integration

package store_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/accountapi"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
)

func TestAccountEmailAndPassword(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	r.user(2, "bob", false, false)
	st := r.db.AccountStore()
	resets := r.db.AuthapiResets()
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	acc, err := st.Account(r.ctx, 1)
	if err != nil || acc.ID != 1 || acc.Email != "ada@example.com" || acc.PasswordHash != "$2a$10$hash" || acc.UsesTOTP || acc.TOTPSecret != "" || acc.UUID == "" {
		t.Fatalf("account = %+v, %v", acc, err)
	}
	for _, id := range []uint64{99, 0, 1 << 40} {
		if _, err := st.Account(r.ctx, id); !errors.Is(err, accountapi.ErrNotFound) {
			t.Errorf("account %d: %v", id, err)
		}
	}

	// A new address: the links asked for the old one stop working, and an address that is taken is refused with nothing changed.
	resets.Replace(r.ctx, "ada@example.com", passwordreset.Record{Hash: "h", CreatedAt: at})
	if err := st.SetEmail(r.ctx, 1, "ada@example.com", "bob@example.com"); !errors.Is(err, accountapi.ErrEmailTaken) {
		t.Errorf("a taken address: %v", err)
	}
	if _, found, _ := resets.Latest(r.ctx, "ada@example.com"); !found {
		t.Error("the links were removed although the change was refused")
	}
	if err := st.SetEmail(r.ctx, 1, "ada@example.com", "ADA2@example.com"); err != nil {
		t.Fatal(err)
	}
	if acc, _ := st.Account(r.ctx, 1); acc.Email != "ADA2@example.com" {
		t.Errorf("email = %q", acc.Email)
	}
	if _, found, _ := resets.Latest(r.ctx, "ada@example.com"); found {
		t.Error("a link for the old address still works")
	}
	if err := st.SetEmail(r.ctx, 99, "x@example.com", "y@example.com"); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}

	// A new password removes the links of the account's address.
	resets.Replace(r.ctx, "ada2@example.com", passwordreset.Record{Hash: "h2", CreatedAt: at})
	if err := st.SetPassword(r.ctx, 1, "$2a$10$new"); err != nil {
		t.Fatal(err)
	}
	if acc, _ := st.Account(r.ctx, 1); acc.PasswordHash != "$2a$10$new" {
		t.Errorf("hash = %q", acc.PasswordHash)
	}
	if _, found, _ := resets.Latest(r.ctx, "ada2@example.com"); found {
		t.Error("a link survived the new password")
	}
	if err := st.SetPassword(r.ctx, 99, "x"); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestAccountTwoFactor(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	st := r.db.AccountStore()
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	// The secret is stored sealed, and not in the old column.
	if err := st.SetTOTPSecret(r.ctx, 1, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	var column sql.NullString
	var sealed string
	r.sql.QueryRow("SELECT totp_secret FROM users WHERE id = 1").Scan(&column)
	r.sql.QueryRow("SELECT ciphertext FROM raptor_secrets WHERE table_name = 'users' AND column_name = 'totp_secret'").Scan(&sealed)
	if column.Valid || sealed == "" || strings.Contains(sealed, "JBSWY3DPEHPK3PXP") {
		t.Errorf("column %v, sealed %q", column, sealed)
	}
	acc, _ := st.Account(r.ctx, 1)
	if acc.TOTPSecret != "JBSWY3DPEHPK3PXP" || acc.UsesTOTP {
		t.Errorf("account = %+v", acc)
	}
	// A secret that was never confirmed is replaced.
	if err := st.SetTOTPSecret(r.ctx, 1, "NEWSECRETNEWSECR"); err != nil {
		t.Fatal(err)
	}
	if acc, _ := st.Account(r.ctx, 1); acc.TOTPSecret != "NEWSECRETNEWSECR" {
		t.Errorf("secret = %q", acc.TOTPSecret)
	}
	var n int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'users'").Scan(&n)
	if n != 1 {
		t.Errorf("%d sealed values for one secret", n)
	}
	if err := st.SetTOTPSecret(r.ctx, 99, "x"); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}

	// Switching it on: the old recovery tokens go, the new hashes are stored, the code that was used cannot be used again.
	r.exec("INSERT INTO recovery_tokens (user_id, token, created_at) VALUES (1, 'old', NOW())")
	if err := st.EnableTOTP(r.ctx, 1, []string{"h1", "h2", "h3"}, at); err != nil {
		t.Fatal(err)
	}
	tokens, _ := r.db.AuthapiRecovery().ForUser(r.ctx, 1)
	if len(tokens) != 3 || tokens[0].Hash != "h1" {
		t.Errorf("tokens = %+v", tokens)
	}
	u, _ := r.db.AuthapiUsers().ByID(r.ctx, 1)
	if !u.UsesTOTP || u.TOTPLastUsed == nil || !u.TOTPLastUsed.Equal(at) || u.TOTPSecret != "NEWSECRETNEWSECR" {
		t.Errorf("user = %+v", u)
	}
	if ok, _ := r.db.AuthapiUsers().UseTOTPStep(r.ctx, 1, at); ok {
		t.Error("the code that switched it on can sign in")
	}
	if err := st.EnableTOTP(r.ctx, 99, []string{"x"}, at); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	var strays int
	r.sql.QueryRow("SELECT COUNT(*) FROM recovery_tokens WHERE user_id = 99").Scan(&strays)
	if strays != 0 {
		t.Errorf("%d recovery tokens for a user that is not there", strays)
	}

	// Switching it off removes everything.
	if err := st.DisableTOTP(r.ctx, 1); err != nil {
		t.Fatal(err)
	}
	u, _ = r.db.AuthapiUsers().ByID(r.ctx, 1)
	r.sql.QueryRow("SELECT COUNT(*) FROM recovery_tokens").Scan(&n)
	var secretsLeft int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'users'").Scan(&secretsLeft)
	if u.UsesTOTP || u.TOTPSecret != "" || n != 0 || secretsLeft != 0 {
		t.Errorf("after: %+v, %d tokens, %d secrets", u, n, secretsLeft)
	}
}

func TestAccountAPIKeys(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	r.user(2, "bob", false, false)
	st := r.db.AccountStore()

	rec, err := st.CreateKey(r.ctx, 1, accountapi.NewKey{Identifier: "ptlc_account0001", SecretHash: auth.HashKeySecret("s1"), Description: "laptop", AllowedIPs: []string{"10.0.0.0/8"}}, auth.CheckCanCreateKey)
	if err != nil || rec.ID == 0 || rec.Identifier != "ptlc_account0001" || rec.CreatedAt.IsZero() {
		t.Fatalf("created = %+v, %v", rec, err)
	}
	// The authorizer finds it, with the hash of the secret and as a key of the account, not of the Application API.
	k, err := r.db.AuthzKeys().ByIdentifier(r.ctx, "ptlc_account0001")
	if err != nil || k.SecretHash != auth.HashKeySecret("s1") || k.Type != auth.KeyAccount || k.Access != nil || len(k.AllowedIPs) != 1 {
		t.Fatalf("key = %+v, %v", k, err)
	}
	// An application key of the same user does not show among the keys of the account, and does not count against the limit.
	r.exec("INSERT INTO api_keys (user_id, key_type, identifier, token, memo) VALUES (1, 2, 'ptla_appkey00001', '', 'app')")
	r.exec("INSERT INTO api_keys (user_id, key_type, identifier, token, memo) VALUES (2, 1, 'ptlc_bobskey0001', '', 'bob')")
	list, err := st.APIKeys(r.ctx, 1)
	if err != nil || len(list) != 1 || list[0].Identifier != "ptlc_account0001" || list[0].Description != "laptop" || len(list[0].AllowedIPs) != 1 {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Another user's key, and a key of the Application API, cannot be deleted through the account.
	for _, ident := range []string{"ptlc_bobskey0001", "ptla_appkey00001", "ptlc_nothere0001"} {
		if _, err := st.DeleteKey(r.ctx, 1, ident); !errors.Is(err, accountapi.ErrNotFound) {
			t.Errorf("%s: %v", ident, err)
		}
	}
	got, err := st.DeleteKey(r.ctx, 1, "ptlc_account0001")
	if err != nil || got.Identifier != "ptlc_account0001" {
		t.Fatalf("deleted = %+v, %v", got, err)
	}
	var secretsLeft int
	r.sql.QueryRow("SELECT COUNT(*) FROM raptor_secrets WHERE table_name = 'api_keys'").Scan(&secretsLeft)
	if secretsLeft != 0 {
		t.Errorf("%d sealed values of a key that is gone", secretsLeft)
	}
	if _, err := r.db.AuthzKeys().ByIdentifier(r.ctx, "ptlc_account0001"); err == nil {
		t.Error("a deleted key still authenticates")
	}
	if _, err := st.CreateKey(r.ctx, 99, accountapi.NewKey{Identifier: "ptlc_ghost000001", SecretHash: "h"}, auth.CheckCanCreateKey); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("a user that is not there: %v", err)
	}
}

// The limit is counted with the user's row locked: a crowd of requests cannot make more keys than the limit.
func TestAccountKeyLimitHoldsUnderLoad(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	st := r.db.AccountStore()
	var seq int32
	wins := once(t, 40, func() (bool, error) {
		n := int(addInt32(&seq))
		_, err := st.CreateKey(r.ctx, 1, accountapi.NewKey{Identifier: "ptlc_load0" + pad(n), SecretHash: "h"}, auth.CheckCanCreateKey)
		if err != nil && !errors.Is(err, auth.ErrTooManyKeys) {
			return false, err
		}
		return err == nil, nil
	})
	var rows int
	r.sql.QueryRow("SELECT COUNT(*) FROM api_keys").Scan(&rows)
	if wins != auth.MaxAccountKeys || rows != auth.MaxAccountKeys {
		t.Errorf("%d keys were made (%d rows), the limit is %d", wins, rows, auth.MaxAccountKeys)
	}
}

func TestAccountSSHKeys(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	r.user(2, "bob", false, false)
	st := r.db.AccountStore()

	k, err := st.AddSSHKey(r.ctx, 1, accountapi.SSHKeyRecord{Name: "laptop", Fingerprint: "SHA256:aaa", PublicKey: "-----BEGIN PUBLIC KEY-----"})
	if err != nil || k.ID == 0 || k.CreatedAt.IsZero() {
		t.Fatalf("added = %+v, %v", k, err)
	}
	if _, err := st.AddSSHKey(r.ctx, 1, accountapi.SSHKeyRecord{Name: "again", Fingerprint: "SHA256:aaa", PublicKey: "x"}); !errors.Is(err, accountapi.ErrKeyExists) {
		t.Errorf("the same fingerprint: %v", err)
	}
	// The same key may belong to another user.
	if _, err := st.AddSSHKey(r.ctx, 2, accountapi.SSHKeyRecord{Name: "bob", Fingerprint: "SHA256:aaa", PublicKey: "x"}); err != nil {
		t.Errorf("another user's copy: %v", err)
	}
	if _, err := st.AddSSHKey(r.ctx, 99, accountapi.SSHKeyRecord{Name: "x", Fingerprint: "SHA256:zzz", PublicKey: "x"}); !errors.Is(err, accountapi.ErrNotFound) {
		t.Errorf("a user that is not there: %v", err)
	}
	has, err := st.HasFingerprint(r.ctx, 1, "SHA256:aaa")
	if err != nil || !has {
		t.Errorf("has = %v, %v", has, err)
	}
	if has, _ := st.HasFingerprint(r.ctx, 1, "SHA256:nope"); has {
		t.Error("a fingerprint that is not there")
	}
	list, err := st.SSHKeys(r.ctx, 1)
	if err != nil || len(list) != 1 || list[0].Name != "laptop" || list[0].Fingerprint != "SHA256:aaa" {
		t.Errorf("list = %+v, %v", list, err)
	}

	// Removing is soft, as in the reference: the row stays, the key is gone for every purpose, and it can be added again.
	if got, found, err := st.RemoveSSHKey(r.ctx, 1, "SHA256:nope"); err != nil || found {
		t.Errorf("an unknown fingerprint: %+v %v %v", got, found, err)
	}
	got, found, err := st.RemoveSSHKey(r.ctx, 1, "SHA256:aaa")
	if err != nil || !found || got.Name != "laptop" {
		t.Fatalf("removed = %+v %v %v", got, found, err)
	}
	var rows, live int
	r.sql.QueryRow("SELECT COUNT(*) FROM user_ssh_keys WHERE user_id = 1").Scan(&rows)
	r.sql.QueryRow("SELECT COUNT(*) FROM user_ssh_keys WHERE user_id = 1 AND deleted_at IS NULL").Scan(&live)
	if rows != 1 || live != 0 {
		t.Errorf("%d rows, %d live", rows, live)
	}
	if fps, _ := r.db.RemoteUsers().Fingerprints(r.ctx, 1); len(fps) != 0 {
		t.Errorf("the node still accepts the key: %v", fps)
	}
	if list, _ := st.SSHKeys(r.ctx, 1); len(list) != 0 {
		t.Errorf("list = %+v", list)
	}
	if _, found, _ := st.RemoveSSHKey(r.ctx, 1, "SHA256:aaa"); found {
		t.Error("removed twice")
	}
	if _, err := st.AddSSHKey(r.ctx, 1, accountapi.SSHKeyRecord{Name: "laptop", Fingerprint: "SHA256:aaa", PublicKey: "x"}); err != nil {
		t.Errorf("adding it again: %v", err)
	}
	// Of 10 requests at once for one new fingerprint, one adds it.
	if n := once(t, 10, func() (bool, error) {
		_, err := st.AddSSHKey(r.ctx, 1, accountapi.SSHKeyRecord{Name: "race", Fingerprint: "SHA256:race", PublicKey: "x"})
		if errors.Is(err, accountapi.ErrKeyExists) {
			return false, nil
		}
		return err == nil, err
	}); n != 1 {
		t.Errorf("%d requests added the same key", n)
	}
}

func TestAccountActivity(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	log := r.db.AccountActivity()
	key := uint64(9)
	for _, e := range []accountapi.Entry{
		{Event: "user:account.email-changed", UserID: 1, IP: "203.0.113.9", Properties: `{"old":"a","new":"b","ip":"203.0.113.9"}`},
		{Event: "user:api-key.create", UserID: 1, SubjectID: 5, APIKeyID: &key, IP: "203.0.113.9", Properties: `{}`},
		{Event: "user:ssh-key.delete", UserID: 1, SubjectID: 6, IP: "203.0.113.9", Properties: `{}`},
	} {
		if err := log.Log(r.ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := r.sql.Query("SELECT l.event, l.actor_id, l.api_key_id, GROUP_CONCAT(CONCAT(SUBSTRING_INDEX(s.subject_type, '\\\\', -1), ':', s.subject_id) ORDER BY s.id) FROM activity_logs l JOIN activity_log_subjects s ON s.activity_log_id = l.id GROUP BY l.id ORDER BY l.id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var event, subjects string
		var actor uint64
		var apiKey sql.NullInt64
		rows.Scan(&event, &actor, &apiKey, &subjects)
		got = append(got, event+"|"+subjects+"|"+map[bool]string{true: "key", false: "nokey"}[apiKey.Valid])
	}
	want := []string{"user:account.email-changed|User:1|nokey", "user:api-key.create|User:1,ApiKey:5|key", "user:ssh-key.delete|User:1,UserSSHKey:6|nokey"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("entries = %q", got)
	}
}

func addInt32(p *int32) int32 { *p++; return *p }

func pad(n int) string {
	s := "0000" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	return s[len(s)-6:]
}

// Keys of the Application API do not count against the limit of the keys of an account.
func TestAccountKeyLimitCountsAccountKeysOnly(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", true, false)
	for i := 0; i < auth.MaxAccountKeys-1; i++ {
		r.exec("INSERT INTO api_keys (user_id, key_type, identifier, token) VALUES (1, 1, ?, '')", "ptlc_fill0"+pad(i))
	}
	for i := 0; i < 10; i++ {
		r.exec("INSERT INTO api_keys (user_id, key_type, identifier, token) VALUES (1, 2, ?, '')", "ptla_fill0"+pad(i))
	}
	st := r.db.AccountStore()
	if _, err := st.CreateKey(r.ctx, 1, accountapi.NewKey{Identifier: "ptlc_lastone0001", SecretHash: "h"}, auth.CheckCanCreateKey); err != nil {
		t.Fatalf("the last key that fits: %v", err)
	}
	if _, err := st.CreateKey(r.ctx, 1, accountapi.NewKey{Identifier: "ptlc_onetoomany1", SecretHash: "h"}, auth.CheckCanCreateKey); !errors.Is(err, auth.ErrTooManyKeys) {
		t.Errorf("one too many: %v", err)
	}
}

// A secret that was never confirmed is replaced, and the time of the last code goes with it.
func TestAccountNewSecretForgetsTheLastCode(t *testing.T) {
	r := newRig(t)
	r.user(1, "ada", false, false)
	st := r.db.AccountStore()
	if err := st.SetTOTPSecret(r.ctx, 1, "FIRSTSECRETFIRST"); err != nil {
		t.Fatal(err)
	}
	r.exec("UPDATE users SET totp_authenticated_at = '2026-11-01 10:00:00' WHERE id = 1")
	if err := st.SetTOTPSecret(r.ctx, 1, "SECONDSECRETSECO"); err != nil {
		t.Fatal(err)
	}
	if u, _ := r.db.AuthapiUsers().ByID(r.ctx, 1); u.TOTPLastUsed != nil {
		t.Errorf("the last code is remembered: %v", u.TOTPLastUsed)
	}
}
