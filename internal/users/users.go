// Package users validates and prepares user accounts. The rules follow the reference panel's User model.
package users

import (
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/ids"
)

const (
	maxLength = 191

	// generatedPasswordLength is the length of the random password of an account that has not got one yet.
	generatedPasswordLength = 30
)

var (
	// usernamePattern is the reference panel's Username rule, applied to the lower cased name.
	usernamePattern = regexp.MustCompile(`^[a-z0-9]([\w.-]+)[a-z0-9]$`)
	languagePattern = regexp.MustCompile(`^[a-z]{2,3}(_[A-Z]{2})?$`)
)

// Input is what an admin provides for a new user. A blank ExternalID means none.
type Input struct {
	ExternalID string
	Email      string
	Username   string
	FirstName  string
	LastName   string
	Password   string
	Language   string // empty means "en"
	Admin      bool
}

// Record is a new user, ready to be stored: the UUID is generated, the username is lower case and the password is
// hashed. NeedsSetup is set when no password was given: the stored one is random and nobody knows it, so the user
// must be sent the account-created mail with a setup token.
type Record struct {
	UUID         string
	ExternalID   *string
	Email        string
	Username     string
	FirstName    string
	LastName     string
	PasswordHash string
	Language     string
	Admin        bool
	NeedsSetup   bool
}

// Prepare validates in and returns the record to store. The password is required. r is the source of randomness.
func Prepare(in Input, r io.Reader) (Record, error) { return prepare(in, nil, true, r) }

// PrepareAccount is Prepare for an account made through the API: the password is optional, and the language must be
// one of languages.
func PrepareAccount(in Input, languages []string, r io.Reader) (Record, error) {
	return prepare(in, languages, false, r)
}

func prepare(in Input, languages []string, passwordRequired bool, r io.Reader) (Record, error) {
	if in.Language == "" {
		in.Language = "en"
	}
	if err := validate(in, languages, passwordRequired); err != nil {
		return Record{}, err
	}

	password, needsSetup := in.Password, in.Password == ""
	if needsSetup {
		var err error
		if password, err = ids.RandomAlnum(r, generatedPasswordLength); err != nil {
			return Record{}, fmt.Errorf("generate password: %w", err)
		}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Record{}, fmt.Errorf("hash password: %w", err)
	}
	uuid, err := ids.UUIDv4(r)
	if err != nil {
		return Record{}, fmt.Errorf("generate uuid: %w", err)
	}

	rec := Record{
		UUID:         uuid,
		Email:        in.Email,
		Username:     strings.ToLower(in.Username),
		FirstName:    in.FirstName,
		LastName:     in.LastName,
		PasswordHash: hash,
		Language:     in.Language,
		Admin:        in.Admin,
		NeedsSetup:   needsSetup,
	}
	if in.ExternalID != "" {
		rec.ExternalID = &in.ExternalID
	}
	return rec, nil
}

// Validate checks every field of a new user against simple allow-lists. The problem found first is the error, a
// *fielderr.Error.
func Validate(in Input) error { return validate(in, nil, true) }

func validate(in Input, languages []string, passwordRequired bool) error {
	if err := validateProfile(in.Email, in.Username, in.FirstName, in.LastName); err != nil {
		return err
	}
	if in.ExternalID != "" {
		if err := ValidateExternalID(in.ExternalID); err != nil {
			return err
		}
	}
	if err := validateLanguage(in.Language, languages); err != nil {
		return err
	}
	if in.Password == "" && !passwordRequired {
		return nil
	}
	return ValidatePassword(in.Password)
}

func validateProfile(email, username, firstName, lastName string) error {
	if err := validateEmail(email); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(username); n < 1 || n > maxLength || !usernamePattern.MatchString(strings.ToLower(username)) {
		return fielderr.New("username", "must start and end with a letter or digit and contain only letters, digits, dashes, underscores and periods (3 to 191 characters)")
	}
	if err := validateName("first_name", firstName); err != nil {
		return err
	}
	return validateName("last_name", lastName)
}

// ValidateExternalID checks the identifier another system gave an account.
func ValidateExternalID(id string) error {
	if n := utf8.RuneCountInString(id); n < 1 || n > maxLength || hasControl(id) {
		return fielderr.New("external_id", "must be 1 to 191 characters without control characters")
	}
	return nil
}

func validateLanguage(language string, available []string) error {
	if !languagePattern.MatchString(language) {
		return fielderr.New("language", "must look like en or pt_BR")
	}
	if available != nil && !slices.Contains(available, language) {
		return fielderr.New("language", "is not one of the available languages")
	}
	return nil
}

// ValidatePassword enforces the length limits for a new password.
func ValidatePassword(password string) error {
	switch {
	case utf8.RuneCountInString(password) < auth.MinPasswordLength:
		return fielderr.New("password", "must be at least %d characters", auth.MinPasswordLength)
	case len(password) > auth.MaxPasswordBytes:
		return fielderr.New("password", "must be at most %d bytes", auth.MaxPasswordBytes)
	}
	return nil
}

func validateEmail(email string) error {
	if utf8.RuneCountInString(email) > maxLength {
		return fielderr.New("email", "must be at most 191 characters")
	}
	addr, err := mail.ParseAddress(email)
	// A bare address only: no display name, no angle brackets, no comments, no whitespace.
	if err != nil || addr.Address != email || strings.ContainsAny(email, " \t\r\n<>(),;:\"") {
		return fielderr.New("email", "must be a valid email address")
	}
	_, domain, _ := strings.Cut(email, "@")
	if !strings.Contains(domain, ".") {
		return fielderr.New("email", "domain must contain a dot")
	}
	return nil
}

func validateName(field, value string) error {
	if n := utf8.RuneCountInString(value); n < 1 || n > maxLength {
		return fielderr.New(field, "must be 1 to 191 characters")
	}
	if hasControl(value) {
		return fielderr.New(field, "must not contain control characters")
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}
