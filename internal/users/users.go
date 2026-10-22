// Package users validates and prepares new user accounts. The rules follow the reference panel's User model.
package users

import (
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/ids"
)

const maxLength = 191

var (
	// usernamePattern is the reference panel's Username rule, applied to the lower cased name.
	usernamePattern = regexp.MustCompile(`^[a-z0-9]([\w.-]+)[a-z0-9]$`)
	languagePattern = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)
)

// Input is what an admin provides for a new user.
type Input struct {
	Email     string
	Username  string
	FirstName string
	LastName  string
	Password  string
	Language  string // empty means "en"
	Admin     bool
}

// Record is a new user, ready to be stored: the UUID is generated and the password is hashed.
type Record struct {
	UUID         string
	Email        string
	Username     string
	FirstName    string
	LastName     string
	PasswordHash string
	Language     string
	Admin        bool
}

// Prepare validates in and returns the record to store. r is the source of randomness for the UUID.
func Prepare(in Input, r io.Reader) (Record, error) {
	if in.Language == "" {
		in.Language = "en"
	}
	if err := Validate(in); err != nil {
		return Record{}, err
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Record{}, fmt.Errorf("hash password: %w", err)
	}
	uuid, err := ids.UUIDv4(r)
	if err != nil {
		return Record{}, fmt.Errorf("generate uuid: %w", err)
	}

	return Record{
		UUID:         uuid,
		Email:        in.Email,
		Username:     in.Username,
		FirstName:    in.FirstName,
		LastName:     in.LastName,
		PasswordHash: hash,
		Language:     in.Language,
		Admin:        in.Admin,
	}, nil
}

// Validate checks every field of a new user against simple allow-lists.
func Validate(in Input) error {
	if err := validateEmail(in.Email); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(in.Username); n < 1 || n > maxLength || !usernamePattern.MatchString(strings.ToLower(in.Username)) {
		return errors.New("username must start and end with a letter or digit and contain only letters, digits, dashes, underscores and periods (3 to 191 characters)")
	}
	if err := validateName("first name", in.FirstName); err != nil {
		return err
	}
	if err := validateName("last name", in.LastName); err != nil {
		return err
	}
	if !languagePattern.MatchString(in.Language) {
		return errors.New("language must look like en or pt_BR")
	}
	return ValidatePassword(in.Password)
}

// ValidatePassword enforces the length limits for a new password.
func ValidatePassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < auth.MinPasswordLength:
		return fmt.Errorf("password must be at least %d characters", auth.MinPasswordLength)
	case len(password) > auth.MaxPasswordBytes:
		return fmt.Errorf("password must be at most %d bytes", auth.MaxPasswordBytes)
	}
	return nil
}

func validateEmail(email string) error {
	if n := utf8.RuneCountInString(email); n < 3 || n > maxLength {
		return errors.New("email must be 3 to 191 characters")
	}
	addr, err := mail.ParseAddress(email)
	// A bare address only: no display name, no angle brackets, no comments, no whitespace.
	if err != nil || addr.Address != email || strings.ContainsAny(email, " \t\r\n<>(),;:\"") {
		return errors.New("email is not a valid address")
	}
	_, domain, _ := strings.Cut(email, "@")
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return errors.New("email domain must contain a dot")
	}
	return nil
}

func validateName(what, value string) error {
	if n := utf8.RuneCountInString(value); n < 1 || n > maxLength {
		return fmt.Errorf("%s must be 1 to 191 characters", what)
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return fmt.Errorf("%s must not contain control characters", what)
	}
	return nil
}
