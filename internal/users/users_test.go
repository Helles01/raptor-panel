package users

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func validInput() Input {
	return Input{Email: "admin@example.com", Username: "admin", FirstName: "Ada", LastName: "Admin", Password: "a long enough password", Admin: true}
}

func TestPrepare(t *testing.T) {
	in := validInput()

	rec, err := Prepare(in, rand.Reader)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	if len(rec.UUID) != 36 {
		t.Errorf("UUID = %q", rec.UUID)
	}
	if rec.Language != "en" {
		t.Errorf("Language = %q, want the default en", rec.Language)
	}
	if rec.Email != in.Email || rec.Username != in.Username || rec.FirstName != in.FirstName || rec.LastName != in.LastName || !rec.Admin {
		t.Errorf("fields not carried over: %+v", rec)
	}
	if rec.PasswordHash == in.Password || strings.Contains(rec.PasswordHash, in.Password) {
		t.Error("the record holds the clear text password")
	}
	if !auth.VerifyPassword(rec.PasswordHash, in.Password) {
		t.Error("the stored hash does not verify against the password")
	}
}

func TestPrepareKeepsGivenLanguage(t *testing.T) {
	in := validInput()
	in.Language = "pt_BR"
	rec, err := Prepare(in, rand.Reader)
	if err != nil || rec.Language != "pt_BR" {
		t.Errorf("Prepare = %+v, %v", rec, err)
	}
}

func TestPrepareFailsOnRandomSourceError(t *testing.T) {
	if _, err := Prepare(validInput(), bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("a failing random source must be an error")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Input)
		wantErr string
	}{
		{"valid", func(*Input) {}, ""},
		{"language set", func(in *Input) { in.Language = "en" }, ""},
		{"unicode names", func(in *Input) { in.FirstName, in.LastName = "Zoë", "Núñez-Åberg" }, ""},
		{"username with dots dashes underscores", func(in *Input) { in.Username = "a.b-c_d" }, ""},
		{"uppercase username", func(in *Input) { in.Username = "Admin01" }, ""},
		{"email with plus", func(in *Input) { in.Email = "ada+panel@example.co.uk" }, ""},

		{"empty email", func(in *Input) { in.Email = "" }, "email"},
		{"no at sign", func(in *Input) { in.Email = "admin.example.com" }, "email"},
		{"display name", func(in *Input) { in.Email = "Ada <ada@example.com>" }, "email"},
		{"no domain dot", func(in *Input) { in.Email = "admin@localhost" }, "dot"},
		{"domain starting with a dot", func(in *Input) { in.Email = "admin@.example.com" }, "email"},
		{"email with space", func(in *Input) { in.Email = "ad min@example.com" }, "email"},
		{"email too long", func(in *Input) { in.Email = strings.Repeat("a", 190) + "@x.io" }, "at most 191"},
		{"email of 192 characters", func(in *Input) { in.Email = strings.Repeat("a", 187) + "@x.io" }, "at most 191"},
		{"email of 191 characters", func(in *Input) { in.Email = strings.Repeat("a", 186) + "@x.io" }, ""},
		{"email of 191 multibyte characters", func(in *Input) { in.Email = strings.Repeat("é", 186) + "@x.io" }, ""},

		{"username too short", func(in *Input) { in.Username = "ab" }, "username"},
		{"username starts with dash", func(in *Input) { in.Username = "-admin" }, "username"},
		{"username ends with dot", func(in *Input) { in.Username = "admin." }, "username"},
		{"username with space", func(in *Input) { in.Username = "ad min" }, "username"},
		{"username with newline at the end", func(in *Input) { in.Username = "admin\n" }, "username"},
		{"username too long", func(in *Input) { in.Username = strings.Repeat("a", 192) }, "username"},
		{"empty username", func(in *Input) { in.Username = "" }, "username"},

		{"empty first name", func(in *Input) { in.FirstName = "" }, "first_name"},
		{"first name too long", func(in *Input) { in.FirstName = strings.Repeat("é", 192) }, "first_name"},
		{"last name with control character", func(in *Input) { in.LastName = "Ad\x00min" }, "last_name"},

		{"bad language", func(in *Input) { in.Language = "english" }, "language"},
		{"language with path", func(in *Input) { in.Language = "../en" }, "language"},

		{"short password", func(in *Input) { in.Password = "short" }, "at least 8"},
		{"password of 7 characters", func(in *Input) { in.Password = "1234567" }, "at least 8"},
		{"password of 8 characters", func(in *Input) { in.Password = "12345678" }, ""},
		{"password of 7 multibyte characters", func(in *Input) { in.Password = "ééééééé" }, "at least 8"},
		{"password of 8 multibyte characters", func(in *Input) { in.Password = "éééééééé" }, ""},
		{"empty password", func(in *Input) { in.Password = "" }, "at least 8"},
		{"password of 72 bytes", func(in *Input) { in.Password = strings.Repeat("a", 72) }, ""},
		{"password of 73 bytes", func(in *Input) { in.Password = strings.Repeat("a", 73) }, "at most 72 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.Language = "en"
			tt.mutate(&in)

			err := Validate(in)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"admin", true},
		{"Admin01", true},
		{"a.b-c_d", true},
		{"abc", true},
		{"ab", false},
		{"-abc", false},
		{"abc.", false},
		{"ab cd", false},
		{"", false},
		{strings.Repeat("a", 191), true},
		{strings.Repeat("a", 192), false},
		{"abc\n", false},
	}
	for _, tt := range tests {
		err := ValidateUsername(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateUsername(%q) = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if fe, isField := fielderr.Of(err); err != nil && (!isField || fe.Field != "username") {
			t.Errorf("ValidateUsername(%q): the error is not about username: %v", tt.name, err)
		}
	}
}
