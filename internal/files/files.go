// Package files holds the rules of the file manager that need no node: which permission each operation needs, and the
// checks of what a request names. The node resolves every path inside the server's own directory; these checks only keep
// out what could never be a valid request.
package files

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/permissions"
)

const (
	MaxPathLength    = 4096
	MaxNameLength    = 255
	MaxFiles         = 1000
	MaxPullURLLength = 2048
	defaultRoot      = "/"
	modeDigitsMin    = 3
	modeDigitsMax    = 4
)

// Operation is something a user does in the file manager.
type Operation string

const (
	OpList            Operation = "list"
	OpContents        Operation = "contents"
	OpDownload        Operation = "download"
	OpWrite           Operation = "write"
	OpCreateDirectory Operation = "create-directory"
	OpRename          Operation = "rename"
	OpCopy            Operation = "copy"
	OpCompress        Operation = "compress"
	OpDecompress      Operation = "decompress"
	OpDelete          Operation = "delete"
	OpChmod           Operation = "chmod"
	OpPull            Operation = "pull"
	OpUpload          Operation = "upload"
)

// The permission of each operation is that of the reference. Two things in it are worth knowing: writing a file needs
// file.create (not file.update, which only rename and chmod need), and decompressing needs file.create (not file.archive).
// So someone with file.create can overwrite a file that exists; see docs/modules/15a-files.md.
var required = map[Operation]string{
	OpList:            permissions.FileRead,
	OpContents:        permissions.FileReadContent,
	OpDownload:        permissions.FileReadContent,
	OpWrite:           permissions.FileCreate,
	OpCreateDirectory: permissions.FileCreate,
	OpRename:          permissions.FileUpdate,
	OpCopy:            permissions.FileCreate,
	OpCompress:        permissions.FileArchive,
	OpDecompress:      permissions.FileCreate,
	OpDelete:          permissions.FileDelete,
	OpChmod:           permissions.FileUpdate,
	OpPull:            permissions.FileCreate,
	OpUpload:          permissions.FileCreate,
}

// PermissionFor returns the permission an operation needs. An operation that is not known has none and is refused.
func PermissionFor(op Operation) (string, bool) {
	p, ok := required[op]
	return p, ok
}

// Root is the directory a request works in: the one given, or "/" when there is none.
func Root(root string) string {
	if root == "" {
		return defaultRoot
	}
	return root
}

// ValidatePath checks a path or a file name from a request: text, not empty, not too long, no control characters. It does
// not look at ".." segments: moving a file up a directory is an ordinary rename, and the node keeps every path inside the
// server's directory.
func ValidatePath(p string) error {
	switch {
	case p == "":
		return errors.New("a path must not be empty")
	case len(p) > MaxPathLength:
		return fmt.Errorf("a path must be at most %d bytes", MaxPathLength)
	case !utf8.ValidString(p) || strings.ContainsFunc(p, isControl):
		return errors.New("a path must be valid text without control characters")
	}
	return nil
}

func isControl(r rune) bool { return r < ' ' || r == 0x7f }

func checkCount(n int) error {
	if n < 1 || n > MaxFiles {
		return fmt.Errorf("send between 1 and %d files", MaxFiles)
	}
	return nil
}

// ValidateFileList checks the root and the files of a delete or compress request.
func ValidateFileList(root string, files []string) error {
	if err := ValidatePath(Root(root)); err != nil {
		return err
	}
	if err := checkCount(len(files)); err != nil {
		return err
	}
	for _, f := range files {
		if err := ValidatePath(f); err != nil {
			return err
		}
	}
	return nil
}

// Rename is one file to move or rename. To may climb out of the root ("../name"), which is how a file is moved up.
type Rename struct{ From, To string }

// ValidateRename checks the root and the items of a rename request.
func ValidateRename(root string, items []Rename) error {
	if err := ValidatePath(Root(root)); err != nil {
		return err
	}
	if err := checkCount(len(items)); err != nil {
		return err
	}
	for _, it := range items {
		if err := ValidatePath(it.From); err != nil {
			return err
		}
		if err := ValidatePath(it.To); err != nil {
			return err
		}
	}
	return nil
}

// Chmod is one file and the mode to give it, as typed: three or four octal digits.
type Chmod struct{ File, Mode string }

// ValidateChmod checks the root and the items of a chmod request. The reference accepts any numeric text ("1e3", "-5",
// "999"), which the node then cannot read as a mode.
func ValidateChmod(root string, items []Chmod) error {
	if err := ValidatePath(Root(root)); err != nil {
		return err
	}
	if err := checkCount(len(items)); err != nil {
		return err
	}
	for _, it := range items {
		if err := ValidatePath(it.File); err != nil {
			return err
		}
		if err := validateMode(it.Mode); err != nil {
			return err
		}
	}
	return nil
}

func validateMode(mode string) error {
	if len(mode) < modeDigitsMin || len(mode) > modeDigitsMax {
		return errors.New("a mode must be three or four octal digits")
	}
	for _, c := range mode {
		if c < '0' || c > '7' {
			return errors.New("a mode must be three or four octal digits")
		}
	}
	return nil
}

// ValidateName checks a single file or folder name, as for a new folder or a pulled file: not a path to somewhere else.
func ValidateName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return errors.New("a name must not be empty, \".\" or \"..\"")
	case len(name) > MaxNameLength:
		return fmt.Errorf("a name must be at most %d bytes", MaxNameLength)
	case strings.ContainsRune(name, '/') || !utf8.ValidString(name) || strings.ContainsFunc(name, isControl):
		return errors.New("a name must be valid text without slashes or control characters")
	}
	return nil
}

// Pull is a request to download a file from the internet into a server. Empty Directory and Filename mean not given.
type Pull struct {
	URL       string
	Directory string
	Filename  string
}

// ValidatePull checks a pull request. The address policy (which hosts a node may reach, whether remote downloads are on at
// all) is the node's own configuration; here only the form of the URL is checked.
func ValidatePull(p Pull) error {
	if err := ValidatePullURL(p.URL); err != nil {
		return err
	}
	if p.Directory != "" {
		if err := ValidatePath(p.Directory); err != nil {
			return err
		}
	}
	if p.Filename != "" {
		return ValidateName(p.Filename)
	}
	return nil
}

// ValidatePullURL accepts an http or https URL with a host and no credentials.
func ValidatePullURL(raw string) error {
	if raw == "" || len(raw) > MaxPullURLLength {
		return fmt.Errorf("the URL must be between 1 and %d characters", MaxPullURLLength)
	}
	// A backslash is a slash to browsers and not to Go, which is how a URL gets read as two different hosts.
	if strings.ContainsFunc(raw, isControl) || strings.ContainsAny(raw, "\\ ") {
		return errors.New("the URL must not contain spaces, backslashes or control characters")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("the URL is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("the URL must start with http:// or https://")
	}
	if u.Hostname() == "" {
		return errors.New("the URL must have a host")
	}
	if u.User != nil {
		return errors.New("the URL must not contain a user name or password")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return errors.New("the URL has an invalid port")
		}
	}
	return nil
}
