package authapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
)

func (r *rig) forgot(email string) reply {
	r.t.Helper()
	return r.do(http.MethodPost, "/auth/password", map[string]string{"email": email})
}

func (r *rig) reset(email, token, password, confirmation string) reply {
	r.t.Helper()
	return r.do(http.MethodPost, "/auth/password/reset", map[string]string{
		"email": email, "token": token, "password": password, "password_confirmation": confirmation,
	})
}

var tokenInLink = regexp.MustCompile(`/auth/password/reset/([0-9a-f]{64})\?email=([^\s"<]+)`)

// requestLink asks for a link and returns the token that the mail carries.
func (r *rig) requestLink(email string) string {
	r.t.Helper()
	if rp := r.forgot(email); rp.Status != http.StatusOK {
		r.t.Fatalf("forgot = %d %s", rp.Status, rp.Body)
	}
	if len(r.outbox.sent) == 0 {
		r.t.Fatal("no mail was queued")
	}
	m := tokenInLink.FindStringSubmatch(r.outbox.sent[len(r.outbox.sent)-1].Text)
	if m == nil {
		r.t.Fatalf("no link in the mail:\n%s", r.outbox.sent[len(r.outbox.sent)-1].Text)
	}
	return m[1]
}

func TestForgotPasswordSendsALink(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.forgot("ada@example.com")

	if rp.Status != http.StatusOK || rp.json()["status"] != "We have e-mailed your password reset link!" {
		t.Fatalf("got %d %s", rp.Status, rp.Body)
	}
	if len(r.outbox.sent) != 1 || r.outbox.sent[0].To != "ada@example.com" || r.outbox.sent[0].Subject != "Reset Password" {
		t.Fatalf("outbox = %+v", r.outbox.sent)
	}
	m := tokenInLink.FindStringSubmatch(r.outbox.sent[0].Text)
	if m == nil {
		t.Fatalf("no link in:\n%s", r.outbox.sent[0].Text)
	}
	if !strings.Contains(r.outbox.sent[0].Text, "https://panel.example.com/auth/password/reset/") {
		t.Error("the link must be built from the configured address")
	}
	if got, _ := url.QueryUnescape(m[2]); got != "ada@example.com" {
		t.Errorf("the link names %q", got)
	}

	rec, ok := r.resets.records["ada@example.com"]
	if !ok || rec.Hash == m[1] || len(rec.Hash) != 64 || !rec.CreatedAt.Equal(t0) {
		t.Errorf("stored record = %+v; the plain token must never be stored", rec)
	}
	if got := r.log.events(); len(got) != 1 || got[0] != "auth:reset-password" || r.log.entries[0].Subject != 1 {
		t.Errorf("events = %v", got)
	}
}

func TestForgotPasswordAnswersTheSameForAnUnknownAddress(t *testing.T) {
	r := newRig(t)
	r.prime()

	known := r.forgot("ada@example.com")
	unknown := r.forgot("nobody@example.com")

	if string(known.Body) != string(unknown.Body) || known.Status != unknown.Status {
		t.Errorf("answers differ:\n%d %s\n%d %s", known.Status, known.Body, unknown.Status, unknown.Body)
	}
	if len(r.outbox.sent) != 1 || len(r.resets.records) != 1 {
		t.Errorf("mails = %d, records = %d; nothing may be stored or sent for an unknown address", len(r.outbox.sent), len(r.resets.records))
	}
	if !strings.Contains(r.logs.String(), "without an account") {
		t.Error("the attempt must be logged")
	}
}

func TestForgotPasswordSendsOneLinkAMinute(t *testing.T) {
	r := newRig(t)
	r.roomy()
	r.forgot("ada@example.com")

	again := r.forgot("ada@example.com")

	if again.Status != http.StatusOK || again.json()["status"] != LinkSent || len(r.outbox.sent) != 1 || r.resets.replaced != 1 {
		t.Errorf("answer %d, mails %d, replaced %d", again.Status, len(r.outbox.sent), r.resets.replaced)
	}

	r.advance(61 * time.Second)
	r.forgot("ada@example.com")
	if len(r.outbox.sent) != 2 || r.resets.replaced != 2 {
		t.Errorf("after a minute: mails %d, replaced %d", len(r.outbox.sent), r.resets.replaced)
	}
}

func TestANewLinkReplacesTheOldOne(t *testing.T) {
	r := newRig(t)
	r.prime()
	first := r.requestLink("ada@example.com")
	r.advance(2 * time.Minute)
	second := r.requestLink("ada@example.com")

	if rp := r.reset("ada@example.com", first, "a new password!", "a new password!"); rp.Status != http.StatusBadRequest {
		t.Errorf("the old token = %d, want it refused", rp.Status)
	}
	if rp := r.reset("ada@example.com", second, "a new password!", "a new password!"); rp.Status != http.StatusOK {
		t.Errorf("the new token = %d %s", rp.Status, rp.Body)
	}
}

func TestForgotPasswordLimitsTheLinksToOneAddress(t *testing.T) {
	r := newRig(t)
	r.roomy()

	for i := 0; i < passwordreset.RequestPerEmailPerHour+3; i++ {
		rp := r.forgot("ada@example.com")
		if rp.Status != http.StatusOK || rp.json()["status"] != LinkSent {
			t.Fatalf("request %d = %d %s; the answer must not change", i, rp.Status, rp.Body)
		}
		r.advance(2 * time.Minute)
	}

	if len(r.outbox.sent) != passwordreset.RequestPerEmailPerHour {
		t.Errorf("%d mails, want %d: the rest must be left unsent", len(r.outbox.sent), passwordreset.RequestPerEmailPerHour)
	}
}

func TestTheLimitOnLinksIsPerAddress(t *testing.T) {
	r := newRig(t)
	r.roomy()
	for i := 0; i < passwordreset.RequestPerEmailPerHour+2; i++ {
		r.forgot("ada@example.com")
		r.advance(2 * time.Minute)
	}
	before := len(r.outbox.sent)

	r.forgot("grace@example.com")

	if len(r.outbox.sent) != before+1 {
		t.Errorf("mails = %d, want one more: the limit of one address must not hold back another", len(r.outbox.sent))
	}
}

func TestForgotPasswordRequestsAreLimitedPerAddress(t *testing.T) {
	r := newRig(t)
	r.prime()
	var last reply
	for i := 0; i < passwordreset.RequestPerIPPerMinute+1; i++ {
		last = r.forgot("nobody@example.com")
	}

	if last.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the 2 a minute limit", last.Status)
	}
}

func TestForgotPasswordChecksTheForm(t *testing.T) {
	r := newRig(t)
	r.roomy()
	bodies := []any{`{}`, `{"email":""}`, `{"email":"nope"}`, `{"email":5}`, `{"email":"Ada <ada@example.com>"}`, `{"email":null}`}

	for _, b := range bodies {
		if rp := r.do(http.MethodPost, "/auth/password", b); rp.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d", b, rp.Status)
		}
	}
	if len(r.outbox.sent) != 0 {
		t.Error("a mail was queued for a bad form")
	}
}

func TestForgotPasswordWithAFailingStore(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.users.failFind = errors.New("db down")
	if rp := r.forgot("ada@example.com"); rp.Status != http.StatusInternalServerError {
		t.Errorf("a failing user store = %d, want 500 and not the usual answer", rp.Status)
	}

	r.users.failFind = nil
	r.resets.latestErr = errors.New("db down")
	if rp := r.forgot("ada@example.com"); rp.Status != http.StatusInternalServerError {
		t.Errorf("a failing token store = %d, want 500", rp.Status)
	}
	if len(r.outbox.sent) != 0 {
		t.Error("a mail was queued although the store failed")
	}
}

func TestForgotPasswordNeedsTheCSRFToken(t *testing.T) {
	r := newRig(t)
	r.prime()

	rp := r.do(http.MethodPost, "/auth/password", map[string]string{"email": "ada@example.com"}, "X-XSRF-TOKEN", "-")

	if rp.Status != http.StatusForbidden || len(r.outbox.sent) != 0 {
		t.Errorf("status = %d, mails = %d", rp.Status, len(r.outbox.sent))
	}
}

func TestResetPasswordSignsInAndEndsEverythingElse(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	r.log.entries = nil

	rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password")

	if rp.Status != http.StatusOK {
		t.Fatalf("status = %d %s", rp.Status, rp.Body)
	}
	body := rp.json()
	if body["success"] != true || body["redirect_to"] != "/" || body["send_to_login"] != false {
		t.Errorf("body = %v", body)
	}
	if !auth.VerifyPassword(r.users.get(1).PasswordHash, "a brand new password") || auth.VerifyPassword(r.users.get(1).PasswordHash, adaPassword) {
		t.Error("the new password is not the one stored")
	}
	if len(r.resets.records) != 0 {
		t.Error("the token must be spent")
	}
	if len(r.nodes.users) != 1 || r.nodes.users[0] != 1 {
		t.Errorf("nodes told about %v", r.nodes.users)
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != 200 || who.json()["user"] != float64(1) {
		t.Errorf("the user must be signed in: %d %s", who.Status, who.Body)
	}
	if got := r.log.events(); len(got) != 1 || got[0] != "auth:password-reset" {
		t.Errorf("events = %v", got)
	}
	if rp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rp.Header.Get("Cache-Control"))
	}
}

func TestResetPasswordRevokesTheUsersSessions(t *testing.T) {
	r := newRig(t)
	r.prime()
	r.login("ada", adaPassword)
	laptop := r.cookie("raptor_session")
	laptopToken := r.cookie("XSRF-TOKEN")

	// The user, on another browser, resets the password.
	phone := r.otherBrowser()
	phone.prime()
	token := phone.requestLink("ada@example.com")
	if rp := phone.reset("ada@example.com", token, "a brand new password", "a brand new password"); rp.Status != http.StatusOK {
		t.Fatalf("reset = %d %s", rp.Status, rp.Body)
	}

	r.client.Jar.SetCookies(mustURL(r.ts.URL), []*http.Cookie{{Name: "raptor_session", Value: laptop, Path: "/"}, {Name: "XSRF-TOKEN", Value: laptopToken, Path: "/"}})
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Errorf("the old session still works: %d", who.Status)
	}
}

func TestResetPasswordForAUserWithTwoFactorDoesNotSignIn(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("grace@example.com")

	rp := r.reset("grace@example.com", token, "a brand new password", "a brand new password")

	if rp.Status != http.StatusOK || rp.json()["send_to_login"] != true {
		t.Fatalf("got %d %s", rp.Status, rp.Body)
	}
	if who := r.do(http.MethodGet, "/whoami", nil); who.Status != http.StatusUnauthorized {
		t.Errorf("a user with two-factor authentication must sign in the ordinary way: %d", who.Status)
	}
	if !auth.VerifyPassword(r.users.get(2).PasswordHash, "a brand new password") {
		t.Error("the password was not changed")
	}
}

func TestResetPasswordRefusesEverythingThatIsNotAGoodToken(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	wrongToken := strings.Repeat("0", 64)

	cases := []struct {
		name, email, token string
		before             func()
	}{
		{"wrong token", "ada@example.com", wrongToken, nil},
		{"malformed token", "ada@example.com", "short", nil},
		{"upper case token", "ada@example.com", strings.ToUpper(token), nil},
		{"the right token for another address", "grace@example.com", token, nil},
		{"unknown address", "nobody@example.com", token, nil},
		{"expired token", "ada@example.com", token, func() { r.advance(61 * time.Minute) }},
	}
	var bodies []string
	for _, tt := range cases {
		if tt.before != nil {
			tt.before()
		}
		rp := r.reset(tt.email, tt.token, "a brand new password", "a brand new password")
		if rp.Status != http.StatusBadRequest || rp.detail() != passwordreset.ErrInvalidToken.Error() {
			t.Errorf("%s: %d %q", tt.name, rp.Status, rp.detail())
		}
		if code := rp.json()["errors"].([]any)[0].(map[string]any)["code"]; code != "DisplayException" {
			t.Errorf("%s: code = %v", tt.name, code)
		}
		bodies = append(bodies, string(rp.Body))
	}
	for _, b := range bodies[1:] {
		if b != bodies[0] {
			t.Errorf("the refusals differ:\n%s\n%s", bodies[0], b)
		}
	}
	if !auth.VerifyPassword(r.users.get(1).PasswordHash, adaPassword) || len(r.nodes.users) != 0 {
		t.Error("a refused reset must change nothing")
	}
}

func TestAResetTokenIsGoodOnce(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	if rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password"); rp.Status != http.StatusOK {
		t.Fatalf("first use = %d %s", rp.Status, rp.Body)
	}
	r.do(http.MethodPost, "/auth/logout", nil)
	r.prime()

	rp := r.reset("ada@example.com", token, "another new password", "another new password")

	if rp.Status != http.StatusBadRequest {
		t.Errorf("second use = %d", rp.Status)
	}
	if !auth.VerifyPassword(r.users.get(1).PasswordHash, "a brand new password") {
		t.Error("the second use changed the password")
	}
}

func TestATokenAnotherRequestSpentFirstDoesNotCount(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	r.resets.consumeLost = true

	rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password")

	if rp.Status != http.StatusBadRequest || !auth.VerifyPassword(r.users.get(1).PasswordHash, adaPassword) {
		t.Errorf("status = %d; the loser of a race must not change the password", rp.Status)
	}
}

func TestResetPasswordChecksTheNewPassword(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")

	tests := []struct {
		name, password, confirmation, want string
	}{
		{"not confirmed", "a brand new password", "another password", "confirmation does not match"},
		{"too short", "short", "short", "at least 8 characters"},
		{"too long", strings.Repeat("a", 73), strings.Repeat("a", 73), "72 bytes"},
		{"empty", "", "", "password field is required"},
	}
	for _, tt := range tests {
		rp := r.reset("ada@example.com", token, tt.password, tt.confirmation)
		if rp.Status != http.StatusUnprocessableEntity || !strings.Contains(string(rp.Body), tt.want) {
			t.Errorf("%s: %d %s", tt.name, rp.Status, rp.Body)
		}
	}
	// None of those spent the token.
	if rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password"); rp.Status != http.StatusOK {
		t.Errorf("the token was used up by a refused form: %d %s", rp.Status, rp.Body)
	}
}

func TestResetPasswordChecksTheForm(t *testing.T) {
	r := newRig(t)
	r.prime()
	bodies := []any{
		`{}`,
		`{"email":"ada@example.com","password":"a brand new password","password_confirmation":"a brand new password"}`,
		`{"token":"x","password":"a brand new password","password_confirmation":"a brand new password"}`,
		`{"token":"x","email":"nope","password":"a brand new password","password_confirmation":"a brand new password"}`,
		`{"token":5,"email":"ada@example.com","password":"a brand new password","password_confirmation":"a brand new password"}`,
		`{"token":"x","email":"ada@example.com","password":["a"],"password_confirmation":"a"}`,
	}

	for _, b := range bodies {
		if rp := r.do(http.MethodPost, "/auth/password/reset", b); rp.Status != http.StatusUnprocessableEntity {
			t.Errorf("%v: status %d", b, rp.Status)
		}
	}
}

func TestResetPasswordGoesOnWhenANodeCannotBeReached(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	r.nodes.err = http.ErrHandlerTimeout

	rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password")

	if rp.Status != http.StatusOK || !auth.VerifyPassword(r.users.get(1).PasswordHash, "a brand new password") {
		t.Errorf("status = %d: the password is changed whether or not the nodes are told", rp.Status)
	}
	if !strings.Contains(r.logs.String(), "a node was not told") {
		t.Error("the failure must be logged")
	}
}

func TestResetPasswordRequestsAreLimitedPerAddress(t *testing.T) {
	r := newRig(t)
	r.prime()
	var last reply
	for i := 0; i < passwordreset.ResetPerIPPerMinute+1; i++ {
		last = r.reset("ada@example.com", strings.Repeat("0", 64), "a brand new password", "a brand new password")
	}

	if last.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the limit on the endpoint that the reference leaves open", last.Status)
	}
}

func TestResetPasswordAnswersDoNotLeakTheToken(t *testing.T) {
	r := newRig(t)
	r.prime()
	token := r.requestLink("ada@example.com")
	rp := r.reset("ada@example.com", token, "a brand new password", "a brand new password")

	for _, secret := range []string{token, "a brand new password"} {
		if strings.Contains(string(rp.Body), secret) || strings.Contains(r.logs.String(), secret) {
			t.Errorf("%q appears in an answer or a log", secret)
		}
	}
}

func TestNewChecksItsDependencies(t *testing.T) {
	good := newRig(t).deps
	tests := []struct {
		name   string
		mutate func(*Deps)
	}{
		{"users", func(d *Deps) { d.Users = nil }},
		{"recovery", func(d *Deps) { d.Recovery = nil }},
		{"resets", func(d *Deps) { d.Resets = nil }},
		{"outbox", func(d *Deps) { d.Outbox = nil }},
		{"nodes", func(d *Deps) { d.Nodes = nil }},
		{"log", func(d *Deps) { d.Log = nil }},
		{"sessions", func(d *Deps) { d.Sessions = nil }},
		{"csrf", func(d *Deps) { d.CSRF = nil }},
		{"failures", func(d *Deps) { d.Failures = nil }},
		{"login limiter", func(d *Deps) { d.LoginLimiter = nil }},
		{"request limiter", func(d *Deps) { d.RequestLimiter = nil }},
		{"reset limiter", func(d *Deps) { d.ResetLimiter = nil }},
		{"email limiter", func(d *Deps) { d.EmailLimiter = nil }},
		{"logger", func(d *Deps) { d.Logger = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := good
			tt.mutate(&d)
			if h, err := New(d); err == nil || h != nil {
				t.Error("New accepted a missing dependency")
			}
		})
	}
}

func TestNewFillsInTheClockAndTheRandomSource(t *testing.T) {
	d := newRig(t).deps
	d.Now, d.Random = nil, nil

	h, err := New(d)
	if err != nil || h.d.Now == nil || h.d.Random == nil {
		t.Fatalf("New = %v, %v", h, err)
	}
	if time.Since(h.d.Now()) > time.Minute {
		t.Error("the default clock is not the real one")
	}
}
