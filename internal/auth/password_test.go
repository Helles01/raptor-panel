package auth

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$2a$10$") {
		t.Errorf("hash = %q, want a $2a$10$ bcrypt hash", hash)
	}
	if strings.Contains(hash, "correct horse") {
		t.Error("hash contains the password")
	}

	tests := []struct {
		name     string
		password string
		want     bool
	}{
		{"right password", "correct horse battery", true},
		{"wrong password", "correct horse batter", false},
		{"empty password", "", false},
		{"case matters", "Correct horse battery", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPassword(hash, tt.password); got != tt.want {
				t.Errorf("VerifyPassword = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("same password")
	b, _ := HashPassword("same password")
	if a == b {
		t.Error("two hashes of one password are identical")
	}
}

func TestVerifyAcceptsPHPHashes(t *testing.T) {
	// From the PHP manual for password_verify(): the hash PHP produced for "rasmuslerdorf".
	const phpHash = "$2y$07$BCryptRequires22Chrcte/VlQH0piJtjXl.0t1XkA8pw9dMXTpOq"

	if !VerifyPassword(phpHash, "rasmuslerdorf") {
		t.Error("a $2y$ hash written by PHP does not verify")
	}
	if VerifyPassword(phpHash, "rasmuslerdorF") {
		t.Error("a $2y$ hash verified with the wrong password")
	}

	// A hash made here, relabelled the way PHP writes it.
	own, err := HashPassword("pässwörd-ß-€")
	if err != nil {
		t.Fatal(err)
	}
	php := "$2y$" + strings.TrimPrefix(own, "$2a$")
	if !VerifyPassword(php, "pässwörd-ß-€") {
		t.Error("a hash relabelled to $2y$ does not verify")
	}
}

func TestLongPasswordsMatchPHPTruncation(t *testing.T) {
	long := strings.Repeat("a", 72)

	// A hash of the first 72 bytes, as PHP would have stored it for a longer password.
	hash, err := HashPassword(long)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, long+"-anything after the 72nd byte") {
		t.Error("bytes after the 72nd must be ignored when verifying, as PHP does")
	}
	if VerifyPassword(hash, long[:71]) {
		t.Error("a shorter password verified")
	}
}

func TestHashPasswordRefusesLongPasswords(t *testing.T) {
	if _, err := HashPassword(strings.Repeat("a", 72)); err != nil {
		t.Errorf("72 bytes must be accepted: %v", err)
	}
	if _, err := HashPassword(strings.Repeat("a", 73)); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("73 bytes: error = %v, want ErrPasswordTooLong", err)
	}
}

func TestVerifyRejectsGarbageHashes(t *testing.T) {
	for _, hash := range []string{"", "plaintext", "$2a$10$tooshort", "$1$md5$hash"} {
		if VerifyPassword(hash, "anything") {
			t.Errorf("VerifyPassword accepted the hash %q", hash)
		}
	}
}

func TestVerifyUnknownUser(t *testing.T) {
	if VerifyUnknownUser("anything") || VerifyUnknownUser("") {
		t.Error("VerifyUnknownUser must never succeed")
	}

	// It must cost about as much as a real check, or it does not hide anything.
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatal(err)
	}
	unknownCost, err := bcrypt.Cost([]byte(unknownUserHash))
	if err != nil {
		t.Fatalf("the stand-in hash is not a bcrypt hash: %v", err)
	}
	if unknownCost != cost {
		t.Errorf("stand-in hash has cost %d, real hashes have %d", unknownCost, cost)
	}
	if VerifyPassword(unknownUserHash, "correct horse battery") {
		t.Error("the stand-in hash matched a password")
	}
}

func TestValidHash(t *testing.T) {
	good, err := HashPassword("a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		hash string
		want bool
	}{
		{"made here", good, true},
		{"from the reference", "$2y" + good[3:], true},
		{"minor b", "$2b" + good[3:], true},
		{"empty", "", false},
		{"plain text", "password123", false},
		{"too short", good[:30], false},
		{"argon2", "$argon2id$v=19$m=65536,t=4,p=1$c29tZXNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", false},
		{"md5 hex", "5f4dcc3b5aa765d61d8327deb882cf99", false},
		{"a cost that is too low", "$2a$02$" + good[7:], false},
	}
	for _, tt := range tests {
		if got := ValidHash(tt.hash); got != tt.want {
			t.Errorf("%s: ValidHash = %v, want %v", tt.name, got, tt.want)
		}
	}
}
