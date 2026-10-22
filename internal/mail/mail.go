// Package mail builds the e-mails the panel sends: their text and HTML, and the message in the form that goes over SMTP. It sends
// nothing itself. Everything a user typed (names, server names) is escaped in the HTML and cleaned of line breaks in the text, and
// every link is built from the panel's configured public address, never from a request.
package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	gomail "net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
)

const (
	maxNameLength = 100
	maxLineLength = 200
)

var serverIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)

// Message is an e-mail ready to be sent.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Site is the panel as the e-mails name it.
type Site struct {
	Name string
	URL  string
}

// NewSite checks the name and the public address of the panel.
func NewSite(name, appURL string) (Site, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLength || !utf8.ValidString(name) || hasControl(name) {
		return Site{}, errors.New("the name of the panel must be 1 to 100 characters without control characters")
	}
	u, err := url.Parse(appURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return Site{}, errors.New("the public address of the panel is not usable")
	}
	return Site{Name: name, URL: u.Scheme + "://" + u.Host}, nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}

// oneLine makes text that a user chose safe to put in a line of an e-mail: control characters, line breaks included, become spaces,
// and it is cut to a sensible length.
func oneLine(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxLineLength {
		s = string([]rune(s)[:maxLineLength]) + "..."
	}
	return s
}

// ValidateAddress checks a recipient or sender: one plain address, no name, no list, nothing that could add a header.
func ValidateAddress(addr string) error {
	if addr == "" || len(addr) > 254 || strings.ContainsAny(addr, "\r\n\x00") {
		return errors.New("not a valid e-mail address")
	}
	a, err := gomail.ParseAddress(addr)
	if err != nil || a.Address != addr {
		return errors.New("not a valid e-mail address")
	}
	return nil
}

type action struct{ Label, URL string }

type content struct {
	Subject  string
	Greeting string
	Lines    []string
	Action   *action
	Error    bool
}

func (s Site) build(to string, c content) (Message, error) {
	if err := ValidateAddress(to); err != nil {
		return Message{}, fmt.Errorf("recipient: %w", err)
	}

	var text strings.Builder
	text.WriteString(c.Greeting + "\n\n")
	for _, l := range c.Lines {
		text.WriteString(l + "\n")
	}
	if c.Action != nil {
		text.WriteString("\n" + c.Action.Label + ": " + c.Action.URL + "\n")
	}
	text.WriteString("\nRegards,\n" + s.Name + "\n")

	var html bytes.Buffer
	data := struct {
		content
		Site string
	}{c, s.Name}
	if err := htmlLayout.Execute(&html, data); err != nil {
		return Message{}, fmt.Errorf("render e-mail: %w", err)
	}
	return Message{To: to, Subject: c.Subject, Text: text.String(), HTML: html.String()}, nil
}

var htmlLayout = htmltemplate.Must(htmltemplate.New("mail").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{.Subject}}</title></head>
<body style="font-family: Arial, Helvetica, sans-serif; color: #333; background: #f5f5f5; margin: 0; padding: 24px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
<table role="presentation" width="570" cellpadding="0" cellspacing="0" style="background: #fff; border-radius: 4px; padding: 32px; max-width: 100%;">
<tr><td>
<h1 style="font-size: 18px; margin: 0 0 16px;">{{.Site}}</h1>
<p style="font-size: 16px; font-weight: bold;">{{.Greeting}}</p>
{{range .Lines}}<p style="font-size: 15px; line-height: 1.5;">{{.}}</p>
{{end}}{{with .Action}}<p style="margin: 24px 0;"><a href="{{.URL}}" style="background: {{if $.Error}}#c0392b{{else}}#2d6cdf{{end}}; color: #fff; padding: 10px 18px; border-radius: 3px; text-decoration: none;">{{.Label}}</a></p>
<p style="font-size: 12px; color: #777;">If you are having trouble clicking the "{{.Label}}" button, copy and paste the address below into your web browser:<br>{{.URL}}</p>
{{end}}<p style="font-size: 15px;">Regards,<br>{{.Site}}</p>
</td></tr></table>
</td></tr></table>
</body></html>
`))

// User is the person an account was made for.
type User struct {
	Name     string
	Username string
	Email    string
}

// PasswordReset is the e-mail with the link to choose a new password. The token must be the 64 characters NewToken makes.
func (s Site) PasswordReset(to, token string) (Message, error) {
	link, err := passwordreset.Link(s.URL, token, to)
	if err != nil {
		return Message{}, err
	}
	return s.build(to, content{
		Subject:  "Reset Password",
		Greeting: "Hello!",
		Lines: []string{
			"You are receiving this email because we received a password reset request for your account.",
			"If you did not request a password reset, no further action is required.",
		},
		Action: &action{Label: "Reset Password", URL: link},
	})
}

// AccountCreated is the e-mail to someone an administrator made an account for. With a token it carries the link to set a password;
// without one it only tells them the account exists.
func (s Site) AccountCreated(u User, setupToken string) (Message, error) {
	c := content{
		Subject:  "Account Created",
		Greeting: "Hello " + oneLine(u.Name) + "!",
		Lines: []string{
			"You are receiving this email because an account has been created for you on " + s.Name + ".",
			"Username: " + oneLine(u.Username),
			"Email: " + oneLine(u.Email),
		},
	}
	if setupToken != "" {
		link, err := passwordreset.Link(s.URL, setupToken, u.Email)
		if err != nil {
			return Message{}, err
		}
		c.Action = &action{Label: "Setup Your Account", URL: link}
	}
	return s.build(u.Email, c)
}

// AddedToServer tells a user that they are now a subuser of a server.
func (s Site) AddedToServer(to, username, serverName, serverShortID string) (Message, error) {
	if !serverIDPattern.MatchString(serverShortID) {
		return Message{}, errors.New("the server identifier is not well formed")
	}
	return s.build(to, content{
		Subject:  "Added To Server",
		Greeting: "Hello " + oneLine(username) + "!",
		Lines: []string{
			"You have been added as a subuser for the following server, allowing you certain control over the server.",
			"Server Name: " + oneLine(serverName),
		},
		Action: &action{Label: "Visit Server", URL: s.URL + "/server/" + serverShortID},
	})
}

// RemovedFromServer tells a user that they are no longer a subuser of a server.
func (s Site) RemovedFromServer(to, username, serverName string) (Message, error) {
	return s.build(to, content{
		Subject:  "Removed From Server",
		Greeting: "Hello " + oneLine(username) + ".",
		Lines: []string{
			"You have been removed as a subuser for the following server.",
			"Server Name: " + oneLine(serverName),
		},
		Action: &action{Label: "Visit Panel", URL: s.URL + "/"},
		Error:  true,
	})
}

// ServerInstalled tells an owner that their server has finished installing.
func (s Site) ServerInstalled(to, username, serverName string) (Message, error) {
	return s.build(to, content{
		Subject:  "Server Installed",
		Greeting: "Hello " + oneLine(username) + ".",
		Lines: []string{
			"Your server has finished installing and is now ready for you to use.",
			"Server Name: " + oneLine(serverName),
		},
		Action: &action{Label: "Login and Begin Using", URL: s.URL + "/"},
	})
}

// Encode writes the message as it goes over SMTP: a multipart/alternative message with a plain text and an HTML part. from is the
// sender's address. r is the source of randomness for the message id and the part boundary (crypto/rand.Reader in production).
func (m Message) Encode(from string, date time.Time, r io.Reader) ([]byte, error) {
	if err := ValidateAddress(from); err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	if err := ValidateAddress(m.To); err != nil {
		return nil, fmt.Errorf("recipient: %w", err)
	}
	if m.Subject == "" || strings.ContainsAny(m.Subject, "\r\n\x00") {
		return nil, errors.New("the subject is empty or has a line break")
	}

	_, domain, _ := strings.Cut(from, "@")
	id, err := randomHex(r, 16)
	if err != nil {
		return nil, err
	}
	boundary, err := randomHex(r, 16)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	hdr := func(k, v string) { buf.WriteString(k + ": " + v + "\r\n") }
	hdr("Date", date.Format(time.RFC1123Z))
	hdr("From", from)
	hdr("To", m.To)
	hdr("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	hdr("Message-ID", "<"+id+"@"+domain+">")
	hdr("MIME-Version", "1.0")
	hdr("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	buf.WriteString("\r\n")

	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		return nil, err
	}
	for _, p := range []struct{ typ, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		part, err := w.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {p.typ + "; charset=UTF-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(part)
		if _, err := qp.Write([]byte(p.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func randomHex(r io.Reader, n int) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
