package mail

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	gomail "net/mail"
	"strings"
	"testing"
	"time"
)

const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func site(t *testing.T) Site {
	t.Helper()
	s, err := NewSite("Raptor Panel", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNewSite(t *testing.T) {
	tests := []struct {
		name string
		app  string
		url  string
		want string
		ok   bool
	}{
		{"plain", "Raptor", "https://panel.example.com", "https://panel.example.com", true},
		{"a trailing slash", "Raptor", "https://panel.example.com/", "https://panel.example.com", true},
		{"a port", "Raptor", "http://localhost:8080", "http://localhost:8080", true},
		{"a path is dropped", "Raptor", "https://panel.example.com/sub/dir", "https://panel.example.com", true},
		{"the longest name", strings.Repeat("a", 100), "https://p.example.com", "https://p.example.com", true},
		{"padded name", "  Raptor  ", "https://p.example.com", "https://p.example.com", true},

		{"no name", "", "https://p.example.com", "", false},
		{"a name of spaces", "   ", "https://p.example.com", "", false},
		{"a long name", strings.Repeat("a", 101), "https://p.example.com", "", false},
		{"a newline in the name", "Rap\ntor", "https://p.example.com", "", false},
		{"no address", "Raptor", "", "", false},
		{"no scheme", "Raptor", "panel.example.com", "", false},
		{"another scheme", "Raptor", "ftp://panel.example.com", "", false},
		{"javascript", "Raptor", "javascript:alert(1)", "", false},
		{"credentials", "Raptor", "https://u:p@panel.example.com", "", false},
		{"no host", "Raptor", "https://", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewSite(tt.app, tt.url)
			if (err == nil) != tt.ok || s.URL != tt.want {
				t.Errorf("NewSite = %+v, %v; want %q, ok=%v", s, err, tt.want, tt.ok)
			}
		})
	}
}

func TestPasswordReset(t *testing.T) {
	m, err := site(t).PasswordReset("jane+x@example.com", token)
	if err != nil {
		t.Fatal(err)
	}
	link := "https://panel.example.com/auth/password/reset/" + token + "?email=jane%2Bx%40example.com"
	if m.To != "jane+x@example.com" || m.Subject != "Reset Password" {
		t.Errorf("message = %+v", m)
	}
	for _, want := range []string{"You are receiving this email because we received a password reset request for your account.", "If you did not request a password reset, no further action is required.", "Reset Password: " + link, "Regards,\nRaptor Panel"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("the text lacks %q:\n%s", want, m.Text)
		}
	}
	if strings.Count(m.HTML, `href="`+link+`"`) != 1 || !strings.Contains(m.HTML, ">Reset Password</a>") {
		t.Errorf("the HTML lacks the button with the link:\n%s", m.HTML)
	}

	for _, bad := range []string{"", "short", strings.ToUpper(token), token + "0", strings.Repeat("g", 64)} {
		if _, err := site(t).PasswordReset("a@example.com", bad); err == nil {
			t.Errorf("a token of %q was accepted", bad)
		}
	}
}

func TestAccountCreated(t *testing.T) {
	u := User{Name: "Jane Doe", Username: "jane", Email: "jane@example.com"}

	m, err := site(t).AccountCreated(u, token)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Account Created" || m.To != "jane@example.com" {
		t.Errorf("message = %+v", m)
	}
	for _, want := range []string{"Hello Jane Doe!", "an account has been created for you on Raptor Panel.", "Username: jane", "Email: jane@example.com", "Setup Your Account: https://panel.example.com/auth/password/reset/" + token} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("the text lacks %q:\n%s", want, m.Text)
		}
	}

	m, err = site(t).AccountCreated(u, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, "Setup Your Account") || strings.Contains(m.HTML, "<a ") {
		t.Errorf("without a token there must be no link:\n%s", m.Text)
	}
	if _, err := site(t).AccountCreated(u, "bad"); err == nil {
		t.Error("a bad token was accepted")
	}
}

func TestSubuserMails(t *testing.T) {
	s := site(t)

	m, err := s.AddedToServer("jane@example.com", "jane", "My server", "abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello jane!", "You have been added as a subuser for the following server", "Server Name: My server", "Visit Server: https://panel.example.com/server/abcd1234"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("AddedToServer lacks %q:\n%s", want, m.Text)
		}
	}
	if m.Subject != "Added To Server" || strings.Contains(m.HTML, "#c0392b") {
		t.Errorf("AddedToServer: subject %q, and it is not an error mail", m.Subject)
	}
	for _, bad := range []string{"", "abcd123", "abcd12345", "abcd-234", "../../x1", "abcd 234"} {
		if _, err := s.AddedToServer("jane@example.com", "jane", "x", bad); err == nil {
			t.Errorf("a server identifier of %q was accepted", bad)
		}
	}

	m, err = s.RemovedFromServer("jane@example.com", "jane", "My server")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello jane.", "You have been removed as a subuser for the following server.", "Server Name: My server", "Visit Panel: https://panel.example.com/"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("RemovedFromServer lacks %q:\n%s", want, m.Text)
		}
	}
	if m.Subject != "Removed From Server" || !strings.Contains(m.HTML, "#c0392b") {
		t.Errorf("RemovedFromServer: subject %q, and it must be an error mail", m.Subject)
	}

	m, err = s.ServerInstalled("jane@example.com", "jane", "My server")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello jane.", "Your server has finished installing and is now ready for you to use.", "Server Name: My server", "Login and Begin Using: https://panel.example.com/"} {
		if !strings.Contains(m.Text, want) {
			t.Errorf("ServerInstalled lacks %q:\n%s", want, m.Text)
		}
	}
	if m.Subject != "Server Installed" {
		t.Errorf("subject = %q", m.Subject)
	}
}

func TestWhatUsersTypeIsEscaped(t *testing.T) {
	evil := `<script>alert("x")</script> & 'q'`
	m, err := site(t).ServerInstalled("jane@example.com", evil, evil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.HTML, "<script>") || strings.Contains(m.HTML, `alert("x")`) {
		t.Errorf("the HTML holds what the user typed unescaped:\n%s", m.HTML)
	}
	if !strings.Contains(m.HTML, "&lt;script&gt;") || !strings.Contains(m.HTML, "&amp;") {
		t.Errorf("the HTML does not show the escaped text:\n%s", m.HTML)
	}
	// The text is plain text: it shows what was typed.
	if !strings.Contains(m.Text, evil) {
		t.Errorf("the text lost what the user typed:\n%s", m.Text)
	}
}

func TestLineBreaksInWhatUsersTypeDoNotMakeNewLines(t *testing.T) {
	m, err := site(t).AddedToServer("jane@example.com", "jane\nBcc: attacker@example.com", "My server\r\n\r\nClick http://evil.example/", "abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(m.Text, "\n") {
		if strings.HasPrefix(line, "Bcc:") || strings.HasPrefix(line, "Click") || strings.HasPrefix(line, "http://evil") {
			t.Errorf("a typed line break made a line of its own: %q", line)
		}
	}
	if !strings.Contains(m.Text, "Hello jane Bcc: attacker@example.com!") || !strings.Contains(m.Text, "Server Name: My server Click http://evil.example/") {
		t.Errorf("the typed text should be on one line:\n%s", m.Text)
	}
}

func TestLongAndOddNames(t *testing.T) {
	m, err := site(t).ServerInstalled("jane@example.com", "jane", strings.Repeat("a", 500))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, strings.Repeat("a", 250)) || !strings.Contains(m.Text, "...") {
		t.Error("a very long name must be cut")
	}

	m, err = site(t).ServerInstalled("jane@example.com", "J\u00e1nos \u00d6", "Caf\u00e9 \u00fczemeltet\u0151")
	if err != nil || !strings.Contains(m.Text, "J\u00e1nos \u00d6") || !strings.Contains(m.HTML, "Caf\u00e9 \u00fczemeltet\u0151") {
		t.Errorf("non-ASCII text must survive: %v\n%s", err, m.Text)
	}

	m, err = site(t).ServerInstalled("jane@example.com", "a\xffb", "x\x00y")
	if err != nil || strings.ContainsRune(m.Text, 0) || strings.ContainsRune(m.Text, '\ufffd') {
		t.Errorf("invalid UTF-8 and NUL must be removed: %v %q", err, m.Text)
	}
}

func TestLinksComeFromTheConfiguredAddressOnly(t *testing.T) {
	s, err := NewSite("Raptor", "https://panel.example.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mustAll(t, s) {
		for _, line := range strings.Split(m.Text, "\n") {
			if i := strings.Index(line, "http"); i >= 0 && !strings.Contains(line, "https://panel.example.com") {
				t.Errorf("a link that is not the panel's: %q", line)
			}
		}
	}
}

func mustAll(t *testing.T, s Site) []Message {
	t.Helper()
	var out []Message
	add := func(m Message, err error) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	add(s.PasswordReset("a@example.com", token))
	add(s.AccountCreated(User{Name: "A", Username: "a", Email: "a@example.com"}, token))
	add(s.AddedToServer("a@example.com", "a", "s", "abcd1234"))
	add(s.RemovedFromServer("a@example.com", "a", "s"))
	add(s.ServerInstalled("a@example.com", "a", "s"))
	return out
}

func TestRecipientsAreChecked(t *testing.T) {
	s := site(t)
	for _, bad := range []string{
		"", "jane", "jane@", "@example.com", "jane@example.com\r\nBcc: x@y.co", "jane@example.com\nBcc: x@y.co", "jane@example.com,john@example.com",
		"Jane <jane@example.com>", "<jane@example.com>", "jane@example.com ", " jane@example.com", "jane@@example.com", "a b@example.com",
		"jane@example.com\x00", strings.Repeat("a", 250) + "@example.com", `"jane"@example.com`,
	} {
		if _, err := s.ServerInstalled(bad, "jane", "s"); err == nil {
			t.Errorf("a recipient of %q was accepted", bad)
		}
		if err := ValidateAddress(bad); err == nil {
			t.Errorf("ValidateAddress(%q) accepted it", bad)
		}
	}
	for _, good := range []string{"jane@example.com", "jane.doe+tag@sub.example.co.uk", "j@e.co", "jane_doe@example.com"} {
		if err := ValidateAddress(good); err != nil {
			t.Errorf("ValidateAddress(%q) = %v", good, err)
		}
	}
}

func parse(t *testing.T, raw []byte) (*gomail.Message, map[string]string) {
	t.Helper()
	msg, err := gomail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the encoded message cannot be read: %v\n%s", err, raw)
	}
	typ, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || typ != "multipart/alternative" {
		t.Fatalf("content type = %q, %v", typ, err)
	}

	parts := map[string]string{}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		parts[pt] = strings.ReplaceAll(string(body), "\r\n", "\n")
	}
	return msg, parts
}

func TestEncode(t *testing.T) {
	m, err := site(t).AddedToServer("jane@example.com", "J\u00e1nos", "Caf\u00e9 server", "abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	m.Subject = "Added To Server \u2013 caf\u00e9"
	date := time.Date(2026, 10, 22, 12, 30, 0, 0, time.UTC)

	raw, err := m.Encode("panel@example.com", date, bytes.NewReader(bytes.Repeat([]byte{0xab}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	msg, parts := parse(t, raw)

	if got := msg.Header.Get("From"); got != "panel@example.com" {
		t.Errorf("From = %q", got)
	}
	if got := msg.Header.Get("To"); got != "jane@example.com" {
		t.Errorf("To = %q", got)
	}
	for _, c := range []byte(msg.Header.Get("Subject")) {
		if c >= 0x80 {
			t.Fatalf("the Subject header holds raw non-ASCII bytes: %q", msg.Header.Get("Subject"))
		}
	}
	dec := new(mime.WordDecoder)
	if subj, err := dec.DecodeHeader(msg.Header.Get("Subject")); err != nil || subj != m.Subject {
		t.Errorf("Subject = %q, %v; want %q", subj, err, m.Subject)
	}
	if got, _ := msg.Header.Date(); !got.Equal(date) {
		t.Errorf("Date = %v", got)
	}
	if got := msg.Header.Get("Message-ID"); got != "<"+strings.Repeat("ab", 16)+"@example.com>" {
		t.Errorf("Message-ID = %q: made from the random source and the sender's domain", got)
	}
	if msg.Header.Get("MIME-Version") != "1.0" {
		t.Error("no MIME-Version")
	}
	if parts["text/plain"] != m.Text || parts["text/html"] != m.HTML {
		t.Errorf("the parts do not decode to the message:\ntext %q\nhtml %q", parts["text/plain"], parts["text/html"])
	}
	if strings.Contains(string(raw), "\n.\n") {
		t.Error("a line that is only a dot would end the message early")
	}
	for _, line := range bytes.Split(raw, []byte("\r\n")) {
		if len(line) > 998 {
			t.Errorf("a line of %d bytes is over the limit of SMTP", len(line))
		}
	}

	// A second message has another id and another boundary.
	raw2, _ := m.Encode("panel@example.com", date, nil)
	raw3, _ := m.Encode("panel@example.com", date, nil)
	if bytes.Equal(raw2, raw3) {
		t.Error("two messages made with the system's randomness are the same")
	}
}

func TestEncodeRefusesAnythingThatCouldAddAHeader(t *testing.T) {
	m := Message{To: "jane@example.com", Subject: "Hello", Text: "t", HTML: "h"}
	date := time.Now()
	if _, err := m.Encode("panel@example.com", date, nil); err != nil {
		t.Fatalf("a good message: %v", err)
	}

	bad := []struct {
		name string
		m    Message
		from string
	}{
		{"a recipient with a line break", Message{To: "jane@example.com\r\nBcc: x@y.co", Subject: "s"}, "panel@example.com"},
		{"a sender with a line break", Message{To: "jane@example.com", Subject: "s"}, "panel@example.com\r\nBcc: x@y.co"},
		{"a subject with a line break", Message{To: "jane@example.com", Subject: "s\r\nBcc: x@y.co"}, "panel@example.com"},
		{"a subject with a newline", Message{To: "jane@example.com", Subject: "s\nBcc: x@y.co"}, "panel@example.com"},
		{"no subject", Message{To: "jane@example.com"}, "panel@example.com"},
		{"no recipient", Message{Subject: "s"}, "panel@example.com"},
		{"no sender", Message{To: "jane@example.com", Subject: "s"}, ""},
		{"a sender with a name", Message{To: "jane@example.com", Subject: "s"}, "Panel <panel@example.com>"},
		{"two recipients", Message{To: "a@example.com, b@example.com", Subject: "s"}, "panel@example.com"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.m.Encode(tt.from, date, nil); err == nil {
				t.Error("the message was encoded")
			}
		})
	}
}

func TestEncodeFailsWhenRandomnessFails(t *testing.T) {
	m := Message{To: "jane@example.com", Subject: "s", Text: "t", HTML: "h"}
	if _, err := m.Encode("panel@example.com", time.Now(), bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	}
	if _, err := m.Encode("panel@example.com", time.Now(), bytes.NewReader(make([]byte, 20))); err == nil {
		t.Error("want an error when the random bytes run out before the boundary")
	}
}
