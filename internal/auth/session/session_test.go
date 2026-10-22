package session

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestManager(t *testing.T, secure bool) (*Manager, *MemoryStore, *clock) {
	t.Helper()
	store := NewMemoryStore()
	m, err := NewManager(store, Options{IdleTimeout: 12 * time.Hour, MaxLifetime: 7 * 24 * time.Hour, Secure: secure})
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	m.now = c.now
	return m, store, c
}

func TestCreateAndLoad(t *testing.T) {
	m, store, _ := newTestManager(t, true)

	s, err := m.Create(ctx, "198.51.100.7", "Mozilla/5.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ID) != 43 || s.UserID != 0 {
		t.Errorf("session = %+v", s)
	}

	got, err := m.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID != s.ID || got.UserID != 0 || got.IP != "198.51.100.7" || got.UserAgent != "Mozilla/5.0" || !got.CreatedAt.Equal(s.CreatedAt) {
		t.Errorf("loaded = %+v, created = %+v", got, s)
	}

	// The store must not hold the cookie value anywhere.
	for _, k := range store.Keys() {
		if strings.Contains(k, s.ID) || k == s.ID || len(k) != 64 {
			t.Errorf("the store key %q is not a hash of the identifier", k)
		}
	}
	r, _ := store.Get(ctx, store.Keys()[0])
	if strings.Contains(string(r.Payload), s.ID) {
		t.Error("the payload contains the identifier")
	}
}

func TestIdentifiersAreRandom(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	a, _ := m.Create(ctx, "", "")
	b, _ := m.Create(ctx, "", "")
	if a.ID == b.ID {
		t.Error("two sessions share an identifier")
	}
}

func TestLoadRejectsMalformedAndUnknownIDs(t *testing.T) {
	m, store, _ := newTestManager(t, true)
	good, _ := m.Create(ctx, "", "")

	for _, id := range []string{
		"", "short", good.ID + "x", good.ID[:42], strings.Repeat("a", 43), // unknown but well formed
		strings.Repeat("!", 43), "../../etc/passwd", good.ID + "\n", " " + good.ID[1:],
	} {
		if _, err := m.Load(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Load(%q) error = %v, want ErrNotFound", id, err)
		}
	}
	if store.Len() != 1 {
		t.Errorf("a bad lookup changed the store: %d records", store.Len())
	}
}

func TestExpiry(t *testing.T) {
	t.Run("idle timeout, refreshed by use", func(t *testing.T) {
		m, store, c := newTestManager(t, true)
		s, _ := m.Create(ctx, "", "")

		c.advance(11 * time.Hour)
		loaded, err := m.Load(ctx, s.ID)
		if err != nil {
			t.Fatalf("after 11 hours: %v", err)
		}
		if err := m.Save(ctx, loaded); err != nil { // use of the session
			t.Fatal(err)
		}
		c.advance(11 * time.Hour)
		if _, err := m.Load(ctx, s.ID); err != nil {
			t.Errorf("22 hours after creation but 11 after the last use: %v", err)
		}

		c.advance(13 * time.Hour)
		if _, err := m.Load(ctx, s.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("after 13 idle hours: %v", err)
		}
		if store.Len() != 0 {
			t.Error("an expired session must be deleted")
		}
	})

	t.Run("maximum lifetime, however active", func(t *testing.T) {
		m, _, c := newTestManager(t, true)
		s, _ := m.Create(ctx, "", "")
		for step := 0; step < 15; step++ { // 165 hours of use, every 11 hours: never idle
			c.advance(11 * time.Hour)
			loaded, err := m.Load(ctx, s.ID)
			if err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
			_ = m.Save(ctx, loaded)
		}
		c.advance(11 * time.Hour) // 176 hours after creation, more than the 168 hour maximum
		if _, err := m.Load(ctx, s.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("a session older than the maximum lifetime was accepted: %v", err)
		}
	})
}

func TestLoginIssuesANewIdentifier(t *testing.T) {
	m, store, _ := newTestManager(t, true)
	guest, _ := m.Create(ctx, "198.51.100.7", "UA")
	guest.Confirmation = &Confirmation{UserID: 5, Token: "t", ExpiresAt: time.Now().Add(time.Hour)}

	user, err := m.Login(ctx, guest, 5)
	if err != nil {
		t.Fatal(err)
	}

	if user.ID == guest.ID || user.UserID != 5 || user.Confirmation != nil || user.IP != "198.51.100.7" {
		t.Errorf("logged-in session = %+v", user)
	}
	// An identifier planted before the login (session fixation) must be dead afterwards.
	if _, err := m.Load(ctx, guest.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old identifier still works: %v", err)
	}
	if got, err := m.Load(ctx, user.ID); err != nil || got.UserID != 5 {
		t.Errorf("the new identifier does not load: %v %+v", err, got)
	}
	if store.Len() != 1 {
		t.Errorf("store has %d records, want 1", store.Len())
	}
	if _, err := m.Login(ctx, guest, 0); err == nil {
		t.Error("a login without a user was accepted")
	}
}

func TestDestroyAndRevokeUser(t *testing.T) {
	m, store, _ := newTestManager(t, true)
	g1, _ := m.Create(ctx, "", "")
	a1, _ := m.Login(ctx, g1, 1)
	g2, _ := m.Create(ctx, "", "")
	a2, _ := m.Login(ctx, g2, 1)
	g3, _ := m.Create(ctx, "", "")
	b1, _ := m.Login(ctx, g3, 2)

	if err := m.Destroy(ctx, a1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(ctx, a1.ID); !errors.Is(err, ErrNotFound) {
		t.Error("a destroyed session still loads")
	}

	if err := m.RevokeUser(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(ctx, a2.ID); !errors.Is(err, ErrNotFound) {
		t.Error("a revoked user's session still loads")
	}
	if _, err := m.Load(ctx, b1.ID); err != nil {
		t.Errorf("another user's session was revoked: %v", err)
	}
	if store.Len() != 1 {
		t.Errorf("store has %d records, want 1", store.Len())
	}
}

func TestCookies(t *testing.T) {
	for _, secure := range []bool{true, false} {
		m, _, _ := newTestManager(t, secure)
		s, _ := m.Create(ctx, "", "")

		c := m.Cookie(s)

		wantName := "raptor_session"
		if secure {
			wantName = "__Host-raptor_session"
		}
		if c.Name != wantName || c.Value != s.ID || c.Path != "/" || c.Domain != "" || !c.HttpOnly || c.Secure != secure ||
			c.SameSite != http.SameSiteLaxMode || c.MaxAge != 7*24*3600 {
			t.Errorf("secure=%v: cookie = %+v", secure, c)
		}

		clear := m.ClearCookie()
		if clear.Name != wantName || clear.Value != "" || clear.MaxAge >= 0 || clear.Path != "/" {
			t.Errorf("secure=%v: clear cookie = %+v", secure, clear)
		}
	}
}

func TestConfirmation(t *testing.T) {
	m, _, c := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")

	token, err := m.StartConfirmation(s, 42)
	if err != nil || len(token) != 64 {
		t.Fatalf("StartConfirmation = %q, %v", token, err)
	}

	if user, ok := m.CheckConfirmation(s, token); !ok || user != 42 {
		t.Errorf("the right token: %d %v", user, ok)
	}
	for _, bad := range []string{"", "wrong", token + "x", token[:63], strings.ToUpper(token)} {
		if _, ok := m.CheckConfirmation(s, bad); ok && bad != token {
			t.Errorf("token %q was accepted", bad)
		}
	}

	// A wrong guess does not use up the confirmation, but time does.
	if _, ok := m.CheckConfirmation(s, token); !ok {
		t.Error("the confirmation did not survive a failed guess")
	}
	c.advance(4*time.Minute + 59*time.Second)
	if _, ok := m.CheckConfirmation(s, token); !ok {
		t.Error("the confirmation expired too early")
	}
	c.advance(2 * time.Second)
	if _, ok := m.CheckConfirmation(s, token); ok {
		t.Error("an expired confirmation was accepted")
	}

	// It survives a round trip through the store, since the second request loads the session again.
	s2, _ := m.Create(ctx, "", "")
	token2, _ := m.StartConfirmation(s2, 7)
	if err := m.Save(ctx, s2); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.Load(ctx, s2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if user, ok := m.CheckConfirmation(loaded, token2); !ok || user != 7 {
		t.Errorf("after reload: %d %v", user, ok)
	}

	noConfirmation, _ := m.Create(ctx, "", "")
	if _, ok := m.CheckConfirmation(noConfirmation, token); ok {
		t.Error("a session without a confirmation accepted a token")
	}
}

func TestCorruptRecordsAreDropped(t *testing.T) {
	m, store, _ := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")
	k := store.Keys()[0]
	r, _ := store.Get(ctx, k)
	r.Payload = []byte("not json")
	_ = store.Put(ctx, r)

	if _, err := m.Load(ctx, s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a corrupt record loaded: %v", err)
	}
	if store.Len() != 0 {
		t.Error("a corrupt record must be deleted")
	}
}

func TestNewManagerValidates(t *testing.T) {
	good := Options{IdleTimeout: time.Hour, MaxLifetime: time.Hour}
	if _, err := NewManager(nil, good); err == nil {
		t.Error("a nil store was accepted")
	}
	for _, o := range []Options{{}, {IdleTimeout: time.Hour}, {MaxLifetime: time.Hour}, {IdleTimeout: 2 * time.Hour, MaxLifetime: time.Hour}} {
		if _, err := NewManager(NewMemoryStore(), o); err == nil {
			t.Errorf("options %+v were accepted", o)
		}
	}
}

func TestLongUserAgentsAreTruncated(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	s, _ := m.Create(ctx, "", strings.Repeat("a", 1000))
	if len(s.UserAgent) != 255 {
		t.Errorf("user agent length = %d, want 255 (the width of the column)", len(s.UserAgent))
	}
}

func TestFailConfirmationCancelsTheLoginAfterTheLimit(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")
	token, err := m.StartConfirmation(s, 42)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i < MaxConfirmationFailures; i++ {
		if !m.FailConfirmation(s) {
			t.Fatalf("the login was cancelled after %d failures, want %d", i, MaxConfirmationFailures)
		}
		if user, ok := m.CheckConfirmation(s, token); !ok || user != 42 {
			t.Fatalf("the right token stopped working after %d failures", i)
		}
	}

	if m.FailConfirmation(s) {
		t.Error("the login is still pending after the last allowed failure")
	}
	if s.Confirmation != nil {
		t.Error("the confirmation was not removed")
	}
	if _, ok := m.CheckConfirmation(s, token); ok {
		t.Error("the right token works after the login was cancelled")
	}
	if m.FailConfirmation(s) {
		t.Error("a login that is not pending cannot be pending after another failure")
	}
}

func TestACancelledConfirmationCannotBeRevivedByItsCounter(t *testing.T) {
	// Even if the confirmation is still in the session, a counter at the limit refuses the token.
	m, _, _ := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")
	token, _ := m.StartConfirmation(s, 42)
	s.Confirmation.Failures = MaxConfirmationFailures
	if _, ok := m.CheckConfirmation(s, token); ok {
		t.Error("a confirmation at its failure limit accepted its token")
	}
}

func TestConfirmationFailuresSurviveASave(t *testing.T) {
	m, _, _ := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")
	m.StartConfirmation(s, 42)
	m.FailConfirmation(s)
	m.FailConfirmation(s)
	if err := m.Save(ctx, s); err != nil {
		t.Fatal(err)
	}

	loaded, err := m.Load(ctx, s.ID)
	if err != nil || loaded.Confirmation == nil || loaded.Confirmation.Failures != 2 {
		t.Fatalf("loaded = %+v, %v; the count of failures must be kept", loaded, err)
	}
}

func TestConfirmationPending(t *testing.T) {
	m, _, c := newTestManager(t, true)
	s, _ := m.Create(ctx, "", "")
	if m.ConfirmationPending(s) {
		t.Error("a session with no login in progress is pending")
	}

	m.StartConfirmation(s, 42)
	if !m.ConfirmationPending(s) {
		t.Error("a login that was just started is not pending")
	}
	c.advance(ConfirmationLifetime + time.Second)
	if m.ConfirmationPending(s) {
		t.Error("an expired login is pending")
	}

	m.StartConfirmation(s, 42)
	s.Confirmation.Failures = MaxConfirmationFailures
	if m.ConfirmationPending(s) {
		t.Error("a login at its failure limit is pending")
	}
	s.Confirmation = &Confirmation{UserID: 0, Token: "x", ExpiresAt: c.now().Add(time.Hour)}
	if m.ConfirmationPending(s) {
		t.Error("a login for no user is pending")
	}
}

func TestConfirmationLimitIsFive(t *testing.T) {
	// Five wrong codes cancel a pending login; the number is part of the sign-in policy and changes only on purpose.
	if MaxConfirmationFailures != 5 {
		t.Errorf("MaxConfirmationFailures = %d, want 5", MaxConfirmationFailures)
	}
}
