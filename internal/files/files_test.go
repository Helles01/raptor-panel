package files

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPermissionFor(t *testing.T) {
	want := map[Operation]string{
		OpList: "file.read", OpContents: "file.read-content", OpDownload: "file.read-content", OpWrite: "file.create",
		OpCreateDirectory: "file.create", OpRename: "file.update", OpCopy: "file.create", OpCompress: "file.archive",
		OpDecompress: "file.create", OpDelete: "file.delete", OpChmod: "file.update", OpPull: "file.create", OpUpload: "file.create",
	}
	for op, perm := range want {
		if got, ok := PermissionFor(op); !ok || got != perm {
			t.Errorf("PermissionFor(%s) = %q, %v; want %q", op, got, ok, perm)
		}
	}
	if len(required) != len(want) {
		t.Errorf("%d operations have a permission, the test knows %d", len(required), len(want))
	}
	for _, op := range []Operation{"", "list ", "LIST", "move", "sftp"} {
		if p, ok := PermissionFor(op); ok || p != "" {
			t.Errorf("PermissionFor(%q) = %q, %v: an unknown operation must have no permission", op, p, ok)
		}
	}
}

// Each request class of the reference names the permission it needs; ours must be the same.
func TestPermissionsMatchTheReference(t *testing.T) {
	dir := filepath.Join("..", "..", "reference", "app", "Http", "Requests", "Api", "Client", "Servers", "Files")
	classes := map[string]Operation{
		"ChmodFilesRequest": OpChmod, "CompressFilesRequest": OpCompress, "CopyFileRequest": OpCopy,
		"CreateFolderRequest": OpCreateDirectory, "DecompressFilesRequest": OpDecompress, "DeleteFileRequest": OpDelete,
		"GetFileContentsRequest": OpContents, "ListFilesRequest": OpList, "PullFileRequest": OpPull,
		"RenameFileRequest": OpRename, "UploadFileRequest": OpUpload, "WriteFileContentRequest": OpWrite,
	}
	re := regexp.MustCompile(`return Permission::ACTION_FILE_([A-Z_]+);`)

	checked := 0
	for class, op := range classes {
		raw, err := os.ReadFile(filepath.Join(dir, class+".php"))
		if err != nil {
			t.Skip("reference/ is not present")
		}
		m := re.FindStringSubmatch(string(raw))
		if m == nil {
			t.Errorf("%s: no permission found", class)
			continue
		}
		want := "file." + strings.ReplaceAll(strings.ToLower(m[1]), "_", "-")
		if got, _ := PermissionFor(op); got != want {
			t.Errorf("%s: reference %q, ours %q", class, want, got)
		}
		checked++
	}
	if checked != len(classes) {
		t.Errorf("checked %d of %d request classes", checked, len(classes))
	}
	// The download link is made by the same request class as reading the contents.
	if d, _ := PermissionFor(OpDownload); d != "file.read-content" {
		t.Errorf("download needs %q", d)
	}
}

func TestRoot(t *testing.T) {
	if Root("") != "/" || Root("/logs") != "/logs" {
		t.Errorf("Root = %q, %q", Root(""), Root("/logs"))
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"root", "/", true},
		{"a file", "/logs/latest.log", true},
		{"a relative path", "plugins/config.yml", true},
		{"spaces and Unicode", "/my world/café été.txt", true},
		{"dot dot segments are the node's business", "../other/file.txt", true},
		{"dots inside a name", "/a..b/..c", true},
		{"a backslash is a legal file name character", `/a\b`, true},
		{"the longest path", "/" + strings.Repeat("a", 4095), true},

		{"empty", "", false},
		{"too long", strings.Repeat("a", 4097), false},
		{"NUL", "/a\x00b", false},
		{"a newline", "/a\nb", false},
		{"a tab", "/a\tb", false},
		{"a delete character", "/a\x7fb", false},
		{"invalid UTF-8", "/a\xffb", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePath(tt.in); (err == nil) != tt.ok {
				t.Errorf("ValidatePath(%q) = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestValidateFileList(t *testing.T) {
	many := make([]string, 1001)
	for i := range many {
		many[i] = "f"
	}
	tests := []struct {
		name  string
		root  string
		files []string
		ok    bool
	}{
		{"one file", "/", []string{"a.txt"}, true},
		{"no root means the root", "", []string{"a.txt"}, true},
		{"many files", "/logs", many[:1000], true},
		{"no files", "/", nil, false},
		{"too many files", "/", many, false},
		{"an empty name", "/", []string{"a.txt", ""}, false},
		{"a bad root", "/a\x00", []string{"a.txt"}, false},
		{"a bad name", "/", []string{"a\nb"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateFileList(tt.root, tt.files); (err == nil) != tt.ok {
				t.Errorf("error = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestValidateRename(t *testing.T) {
	tests := []struct {
		name  string
		root  string
		items []Rename
		ok    bool
	}{
		{"a rename", "/", []Rename{{"a.txt", "b.txt"}}, true},
		{"a move up one directory", "/plugins", []Rename{{"a.jar", "../a.jar"}}, true},
		{"a move into a directory", "/", []Rename{{"a.jar", "plugins/a.jar"}}, true},
		{"no items", "/", nil, false},
		{"no source", "/", []Rename{{"", "b.txt"}}, false},
		{"no target", "/", []Rename{{"a.txt", ""}}, false},
		{"a bad target", "/", []Rename{{"a.txt", "b\x00"}}, false},
		{"a bad root", "\n", []Rename{{"a.txt", "b.txt"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRename(tt.root, tt.items); (err == nil) != tt.ok {
				t.Errorf("error = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestValidateChmod(t *testing.T) {
	tests := []struct {
		name string
		mode string
		ok   bool
	}{
		{"three digits", "644", true},
		{"four digits", "0755", true},
		{"all rights", "777", true},
		{"none", "000", true},
		{"two digits", "64", false},
		{"five digits", "00755", false},
		{"an eight", "758", false},
		{"a nine", "999", false},
		{"a letter", "rwx", false},
		{"an exponent", "1e3", false},
		{"negative", "-644", false},
		{"a space", "644 ", false},
		{"empty", "", false},
		{"Unicode digits", "٦٤٤", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateChmod("/", []Chmod{{File: "a.txt", Mode: tt.mode}})
			if (err == nil) != tt.ok {
				t.Errorf("mode %q: error = %v, want ok=%v", tt.mode, err, tt.ok)
			}
		})
	}
	if ValidateChmod("/", nil) == nil || ValidateChmod("/", []Chmod{{File: "", Mode: "644"}}) == nil || ValidateChmod("\x00", []Chmod{{File: "a", Mode: "644"}}) == nil {
		t.Error("no items, an empty file and a bad root must be refused")
	}
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{"world", true}, {"my folder", true}, {"café", true}, {strings.Repeat("a", 255), true}, {".hidden", true}, {"a..b", true},
		{"", false}, {".", false}, {"..", false}, {strings.Repeat("a", 256), false}, {"a/b", false}, {"/a", false},
		{"a\x00b", false}, {"a\nb", false}, {"a\xffb", false},
	}
	for _, tt := range tests {
		if err := ValidateName(tt.in); (err == nil) != tt.ok {
			t.Errorf("ValidateName(%q) = %v, want ok=%v", tt.in, err, tt.ok)
		}
	}
}

func TestValidatePullURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"https", "https://example.com/file.zip", true},
		{"http", "http://example.com/file.zip", true},
		{"a port", "https://example.com:8443/a", true},
		{"a query", "https://example.com/a?token=1&x=2", true},
		{"an IPv4 address", "http://203.0.113.5/a", true},
		{"an IPv6 address", "http://[2001:db8::1]:8080/a", true},
		{"upper-case scheme", "HTTPS://example.com/a", true},
		{"percent-encoding", "https://example.com/a%20b.zip", true},

		{"empty", "", false},
		{"too long", "https://example.com/" + strings.Repeat("a", 2048), false},
		{"ftp", "ftp://example.com/a", false},
		{"file", "file:///etc/passwd", false},
		{"gopher", "gopher://example.com/a", false},
		{"javascript", "javascript:alert(1)", false},
		{"data", "data:text/plain,hi", false},
		{"no scheme", "example.com/a", false},
		{"a scheme-relative URL", "//example.com/a", false},
		{"opaque", "https:example.com", false},
		{"no host", "https:///a", false},
		{"credentials", "https://user:pass@example.com/a", false},
		{"a user name only", "https://user@example.com/a", false},
		{"a backslash", `https://example.com\@evil.com/a`, false},
		{"a space", "https://example.com/a b", false},
		{"a newline", "https://example.com/a\nb", false},
		{"a NUL", "https://example.com/a\x00", false},
		{"port zero", "https://example.com:0/a", false},
		{"port too large", "https://example.com:65536/a", false},
		{"a port that is not a number", "https://example.com:http/a", false},
		{"an invalid host", "https://exa mple.com/", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePullURL(tt.in); (err == nil) != tt.ok {
				t.Errorf("ValidatePullURL(%q) = %v, want ok=%v", tt.in, err, tt.ok)
			}
		})
	}
}

func TestValidatePull(t *testing.T) {
	good := Pull{URL: "https://example.com/a.zip", Directory: "/plugins", Filename: "a.zip"}
	with := func(f func(*Pull)) Pull { p := good; f(&p); return p }

	tests := []struct {
		name string
		p    Pull
		ok   bool
	}{
		{"everything", good, true},
		{"only a URL", Pull{URL: "https://example.com/a.zip"}, true},
		{"a bad URL", with(func(p *Pull) { p.URL = "ftp://example.com/a" }), false},
		{"a bad directory", with(func(p *Pull) { p.Directory = "/a\x00" }), false},
		{"a filename that is a path", with(func(p *Pull) { p.Filename = "../a.zip" }), false},
		{"a filename of dots", with(func(p *Pull) { p.Filename = ".." }), false},
		{"a long filename", with(func(p *Pull) { p.Filename = strings.Repeat("a", 256) }), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePull(tt.p); (err == nil) != tt.ok {
				t.Errorf("ValidatePull = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}
