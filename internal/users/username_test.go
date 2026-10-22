package users

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
)

// fixedBytes feeds the random generator a known sequence; 0, 1 and 2 give the characters A, B and C.
func fixedBytes() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{0, 1, 2}, 20)) }

func TestGenerateUsername(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{"plain", "jane@example.com", "janeabc"},
		{"dots and dashes stay", "jane.doe-smith@example.com", "jane.doe-smithabc"},
		{"plus tag is cut", "jane+news@example.com", "janenewsabc"},
		{"case is kept", "Jane.Doe@example.com", "Jane.Doeabc"},
		{"only the part before the first @", "a@b@c.com", "aabc"},
		{"leading punctuation is cut", ".-_jane@example.com", "janeabc"},
		{"nothing usable left", "+++@example.com", "abc"},
		{"empty local part", "@example.com", "abc"},
		{"no @ at all", "jane", "janeabc"},
		{"non-ASCII letters are cut", "jáne@example.com", "jneabc"},
		{"digits first", "42@example.com", "42abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GenerateUsername(tt.email, fixedBytes())
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("GenerateUsername(%q) = %q, want %q", tt.email, got, tt.want)
			}
		})
	}
}

func TestGenerateUsernameIsCapped(t *testing.T) {
	got, err := GenerateUsername(strings.Repeat("a", 100)+"@example.com", fixedBytes())
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Repeat("a", 64) + "abc"; got != want {
		t.Errorf("got %q (%d characters), want 64 characters and the suffix", got, len(got))
	}
}

func TestGenerateUsernameAlwaysValid(t *testing.T) {
	emails := []string{
		"jane@example.com", ".jane@example.com", "_@example.com", "-@example.com", "+@example.com", "@example.com", "jane.",
		"j@example.com", "a-b_c.d@example.com", strings.Repeat("-", 80) + "x@example.com", strings.Repeat("a.", 60) + "@example.com",
		"jane+tag@example.com", "Jáne@example.com", "ő@example.com", "..@example.com",
	}
	for _, email := range emails {
		for i := 0; i < 50; i++ {
			name, err := GenerateUsername(email, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			in := validInput()
			in.Language = "en" // Prepare fills this in; Validate wants it
			in.Username = name
			if err := Validate(in); err != nil {
				t.Fatalf("GenerateUsername(%q) = %q, which does not validate: %v", email, name, err)
			}
		}
	}
}

func TestGenerateUsernameVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		name, err := GenerateUsername("jane@example.com", rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		seen[name] = true
	}
	if len(seen) < 100 {
		t.Errorf("only %d different names in 200 tries", len(seen))
	}
}

func TestGenerateUsernameRandomnessFailure(t *testing.T) {
	if _, err := GenerateUsername("jane@example.com", bytes.NewReader(nil)); err == nil {
		t.Error("want an error when no random bytes can be read")
	} else if errors.Unwrap(err) == nil {
		t.Errorf("the error %q does not wrap its cause", err)
	}
}
