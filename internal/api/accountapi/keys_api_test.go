package accountapi

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/auth"
)

func TestListKeysIsEmptyAtFirst(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "GET", "/api-keys", "")
	if rp.Status != 200 || rp.At("object") != "list" || rp.At("data") == nil || len(rp.At("data").([]any)) != 0 {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
}

func TestCreateKey(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/api-keys", `{"description":"deploy script","allowed_ips":["203.0.113.0/24","198.51.100.7"]}`)
	if rp.Status != 200 || rp.At("object") != "api_key" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	identifier, _ := rp.At("attributes", "identifier").(string)
	token, _ := rp.At("meta", "secret_token").(string)
	if len(identifier) != 16 || !strings.HasPrefix(identifier, "ptlc_") || len(token) != 48 || !strings.HasPrefix(token, identifier) {
		t.Fatalf("identifier %q, token %q", identifier, token)
	}
	if rp.At("attributes", "description") != "deploy script" || rp.At("attributes", "last_used_at") != nil ||
		rp.At("attributes", "created_at") != "2026-10-23T12:00:00+00:00" || !reflect.DeepEqual(rp.At("attributes", "allowed_ips"), []any{"203.0.113.0/24", "198.51.100.7"}) {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	// Only the hash of the secret is kept, and it is the one the authorizer compares with.
	secret := token[16:]
	if len(r.store.hashes) != 1 || r.store.hashes[0] != auth.HashKeySecret(secret) || strings.Contains(r.store.hashes[0], secret) {
		t.Errorf("stored hashes = %v", r.store.hashes)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:api-key.create" || r.activity.entries[0].SubjectID == 0 ||
		r.activity.entries[0].Properties != `{"identifier":"`+identifier+`"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// The secret is not in the list, only the identifier.
	list := r.do(ada, "GET", "/api-keys", "")
	if list.Contains(secret) || list.At("data", 0, "attributes", "identifier") != identifier {
		t.Errorf("list = %s", list.Raw)
	}
}

func TestCreateKeyWithoutAddresses(t *testing.T) {
	for _, body := range []string{`{"description":"x"}`, `{"description":"x","allowed_ips":null}`, `{"description":"x","allowed_ips":[]}`} {
		r := newRig(t)
		rp := r.do(ada, "POST", "/api-keys", body)
		ips, ok := rp.At("attributes", "allowed_ips").([]any)
		if rp.Status != 200 || !ok || len(ips) != 0 {
			t.Errorf("%s: answer = %d %s", body, rp.Status, rp.Raw)
		}
	}
}

func TestCreateKeyRefusals(t *testing.T) {
	many := make([]string, 51)
	for i := range many {
		many[i] = `"203.0.113.9"`
	}
	tests := []struct {
		name   string
		body   string
		detail string
		field  string
	}{
		{"no description", `{}`, "The description field is required.", "description"},
		{"a blank description", `{"description":"  "}`, "The description field is required.", "description"},
		{"a description too long", `{"description":"` + strings.Repeat("a", 501) + `"}`, "The description may not be greater than 500 characters.", "description"},
		{"a description that is a number", `{"description":5}`, "The description must be a string.", "description"},
		{"addresses that are not a list", `{"description":"x","allowed_ips":"203.0.113.9"}`, "The allowed ips must be an array.", "allowed_ips"},
		{"an address that is a number", `{"description":"x","allowed_ips":[5]}`, "The allowed_ips.0 must be a string.", "allowed_ips"},
		{"an address that is not one", `{"description":"x","allowed_ips":["203.0.113.9","nonsense"]}`, "", "allowed_ips"},
		{"too many addresses", `{"description":"x","allowed_ips":[` + strings.Join(many, ",") + `]}`, "The allowed ips may not have more than 50 items.", "allowed_ips"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			rp := r.do(ada, "POST", "/api-keys", tt.body)
			if rp.Status != 422 || (tt.detail != "" && rp.Detail() != tt.detail) || rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.keys[ada]) != 0 || len(r.activity.entries) != 0 {
				t.Error("a key was stored or logged")
			}
		})
	}
	r := newRig(t)
	if rp := r.do(ada, "POST", "/api-keys", `description=x`); rp.Status != 400 {
		t.Errorf("not JSON: status = %d", rp.Status)
	}
}

func TestTheLimitOfKeys(t *testing.T) {
	r := newRig(t)
	for i := 0; i < auth.MaxAccountKeys; i++ {
		r.store.keys[ada] = append(r.store.keys[ada], KeyRecord{ID: uint64(100 + i), Identifier: "ptlc_x" + strings.Repeat("a", 10)[:9], CreatedAt: apitest.Now})
	}
	rp := r.do(ada, "POST", "/api-keys", `{"description":"one too many"}`)
	if rp.Status != 400 || rp.Code() != "DisplayException" || rp.Detail() != "You have reached the account limit for number of API keys." {
		t.Errorf("answer = %d %s", rp.Status, rp.Raw)
	}
	if len(r.store.keys[ada]) != auth.MaxAccountKeys || len(r.activity.entries) != 0 {
		t.Error("a key was stored or logged over the limit")
	}

	// One less and it works: the count is made where the keys are locked.
	r.store.keys[ada] = r.store.keys[ada][1:]
	if rp := r.do(ada, "POST", "/api-keys", `{"description":"the last one"}`); rp.Status != 200 {
		t.Errorf("status = %d: %s", rp.Status, rp.Raw)
	}
	if r.store.keyChecks[len(r.store.keyChecks)-1] != auth.MaxAccountKeys-1 {
		t.Errorf("checks = %v", r.store.keyChecks)
	}
}

func TestKeysShowTheirLastUse(t *testing.T) {
	r := newRig(t)
	used := apitest.Now.Add(-time.Hour)
	r.store.keys[ada] = []KeyRecord{{ID: 5, Identifier: "ptlc_abcdefghijk", Description: "old", LastUsedAt: &used, CreatedAt: apitest.Now.Add(-48 * time.Hour)}}
	rp := r.do(ada, "GET", "/api-keys", "")
	if rp.At("data", 0, "attributes", "last_used_at") != "2026-10-23T11:00:00+00:00" || rp.At("data", 0, "attributes", "created_at") != "2026-10-21T12:00:00+00:00" {
		t.Errorf("attributes = %v", rp.At("data", 0, "attributes"))
	}
	if ips, ok := rp.At("data", 0, "attributes", "allowed_ips").([]any); !ok || len(ips) != 0 {
		t.Errorf("allowed_ips = %v: a key with none lists none", rp.At("data", 0, "attributes", "allowed_ips"))
	}
}

func TestDeleteKey(t *testing.T) {
	r := newRig(t)
	r.store.keys[ada] = []KeyRecord{{ID: 5, Identifier: "ptlc_abcdefghijk"}, {ID: 6, Identifier: "ptlc_lmnopqrstuv"}}
	r.store.keys[bob] = []KeyRecord{{ID: 7, Identifier: "ptlc_bobsbobsbob"}}

	rp := r.do(ada, "DELETE", "/api-keys/ptlc_abcdefghijk", "")
	if rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.keys[ada]) != 1 || r.store.keys[ada][0].Identifier != "ptlc_lmnopqrstuv" {
		t.Errorf("keys = %+v", r.store.keys[ada])
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:api-key.delete" || r.activity.entries[0].SubjectID != 5 ||
		r.activity.entries[0].Properties != `{"identifier":"ptlc_abcdefghijk"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// Another user's key, and one that is not there, are the same.
	for _, id := range []string{"ptlc_bobsbobsbob", "ptlc_nothingatall", "bad%20id", "ptlc;drop"} {
		if rp := r.do(ada, "DELETE", "/api-keys/"+id, ""); rp.Status != 404 {
			t.Errorf("%s: status = %d", id, rp.Status)
		}
	}
	if len(r.store.keys[bob]) != 1 || len(r.activity.entries) != 1 {
		t.Error("another user's key was touched")
	}
}

func TestSSHKeys(t *testing.T) {
	r := newRig(t)
	rp := r.do(ada, "POST", "/ssh-keys", js(`{"name":"laptop","public_key":%q}`, ed25519Line+"\n"))
	if rp.Status != 200 || rp.At("object") != "ssh_key" {
		t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
	}
	if rp.At("attributes", "name") != "laptop" || rp.At("attributes", "fingerprint") != ed25519Fingerprint || rp.At("attributes", "created_at") != "2026-10-23T12:00:17+00:00" {
		t.Errorf("attributes = %v", rp.At("attributes"))
	}
	if pem, _ := rp.At("attributes", "public_key").(string); !strings.HasPrefix(pem, "-----BEGIN PUBLIC KEY-----") {
		t.Errorf("public_key = %q: keys are stored as PEM", pem)
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:ssh-key.create" || r.activity.entries[0].Properties != `{"fingerprint":"`+ed25519Fingerprint+`"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	list := r.do(ada, "GET", "/ssh-keys", "")
	if list.At("object") != "list" || list.At("data", 0, "attributes", "fingerprint") != ed25519Fingerprint {
		t.Errorf("list = %s", list.Raw)
	}
	if other := r.do(bob, "GET", "/ssh-keys", ""); len(other.At("data").([]any)) != 0 {
		t.Errorf("another user sees %s", other.Raw)
	}
	// Another user may register the same key.
	if rp := r.do(bob, "POST", "/ssh-keys", js(`{"name":"same","public_key":%q}`, ed25519Line)); rp.Status != 200 {
		t.Errorf("another user: status = %d: %s", rp.Status, rp.Raw)
	}
}

func TestSSHKeyRefusals(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		detail string
		field  string
	}{
		{"a key already there", js(`{"name":"again","public_key":%q}`, ed25519Line), "The public key provided already exists on your account.", "public_key"},
		{"text that is not a key", `{"name":"x","public_key":"not a key"}`, "The public key provided is not valid.", "public_key"},
		{"an RSA key that is too short", js(`{"name":"x","public_key":%q}`, rsa1024Line), "RSA keys must be at least 2048 bits in length.", "public_key"},
		{"no name", js(`{"public_key":%q}`, ed25519Line), "The name field is required.", "name"},
		{"a name too long", js(`{"name":%q,"public_key":%q}`, strings.Repeat("a", 192), ed25519Line), "The name must be between 1 and 191 characters.", "name"},
		{"no key", `{"name":"x"}`, "The public key field is required.", "public_key"},
		{"a key that is a number", `{"name":"x","public_key":5}`, "The public key must be a string.", "public_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t)
			r.store.ssh[ada] = []SSHKeyRecord{{ID: 1, Name: "first", Fingerprint: ed25519Fingerprint}}
			rp := r.do(ada, "POST", "/ssh-keys", tt.body)
			if rp.Status != 422 || rp.Detail() != tt.detail || rp.At("errors", 0, "meta", "source_field") != tt.field {
				t.Fatalf("answer = %d %s", rp.Status, rp.Raw)
			}
			if len(r.store.ssh[ada]) != 1 || len(r.activity.entries) != 0 {
				t.Error("a key was stored or logged")
			}
		})
	}

	t.Run("two requests added the same key", func(t *testing.T) {
		r := newRig(t)
		r.store.keyExistsRace = true
		rp := r.do(ada, "POST", "/ssh-keys", js(`{"name":"x","public_key":%q}`, ed25519Line))
		if rp.Status != 422 || rp.Detail() != "The public key provided already exists on your account." {
			t.Errorf("answer = %d %s", rp.Status, rp.Raw)
		}
	})
}

func TestRemoveSSHKey(t *testing.T) {
	r := newRig(t)
	r.store.ssh[ada] = []SSHKeyRecord{{ID: 3, Name: "laptop", Fingerprint: "abc"}}
	r.store.ssh[bob] = []SSHKeyRecord{{ID: 4, Name: "bobs", Fingerprint: "xyz"}}

	if rp := r.do(ada, "POST", "/ssh-keys/remove", `{"fingerprint":"abc"}`); rp.Status != 204 || len(rp.Raw) != 0 {
		t.Fatalf("status = %d: %s", rp.Status, rp.Raw)
	}
	if len(r.store.ssh[ada]) != 0 {
		t.Errorf("keys = %+v", r.store.ssh[ada])
	}
	if len(r.activity.entries) != 1 || r.activity.entries[0].Event != "user:ssh-key.delete" || r.activity.entries[0].SubjectID != 3 ||
		r.activity.entries[0].Properties != `{"fingerprint":"abc"}` {
		t.Errorf("entries = %+v", r.activity.entries)
	}

	// A fingerprint the user has no key for, also another user's, is not an error and removes nothing.
	for _, fp := range []string{"abc", "xyz", "nothing"} {
		if rp := r.do(ada, "POST", "/ssh-keys/remove", js(`{"fingerprint":%q}`, fp)); rp.Status != 204 {
			t.Errorf("%s: status = %d", fp, rp.Status)
		}
	}
	if len(r.store.ssh[bob]) != 1 || len(r.activity.entries) != 1 {
		t.Errorf("another user's key was touched or an entry written: %+v", r.activity.entries)
	}

	for _, body := range []string{`{}`, `{"fingerprint":5}`, `{"fingerprint":"  "}`} {
		if rp := r.do(ada, "POST", "/ssh-keys/remove", body); rp.Status != 422 {
			t.Errorf("%s: status = %d", body, rp.Status)
		}
	}
}
