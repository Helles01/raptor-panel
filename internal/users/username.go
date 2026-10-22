package users

import (
	"fmt"
	"io"
	"strings"

	"github.com/Helles01/raptor-panel/internal/ids"
)

const (
	maxGeneratedBase = 64
	suffixLength     = 3
)

// GenerateUsername makes the username of an account that is created because its email address is added as a subuser of a
// server: the part of the address before the @, reduced to letters, digits, dots, dashes and underscores, at most 64
// characters, with three random characters appended to make a clash unlikely. A clash is still possible, so whoever stores
// the account must try again with a new name when the username is taken.
//
// Unlike in the reference, the name always passes Validate: characters that cannot start a username are cut from the
// front, and the random characters are lower case letters and digits (the reference's can be upper case).
func GenerateUsername(email string, r io.Reader) (string, error) {
	local, _, _ := strings.Cut(email, "@")

	base := strings.Map(func(c rune) rune {
		if isAlnum(c) || c == '.' || c == '-' || c == '_' {
			return c
		}
		return -1
	}, local)
	base = strings.TrimLeft(base, "._-")
	if len(base) > maxGeneratedBase {
		base = base[:maxGeneratedBase]
	}

	suffix, err := ids.RandomAlnum(r, suffixLength)
	if err != nil {
		return "", fmt.Errorf("generate username: %w", err)
	}
	return base + strings.ToLower(suffix), nil
}

func isAlnum(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
