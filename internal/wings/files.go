package wings

import (
	"context"
	"net/http"
	"net/url"
)

// FileObject is an entry of a directory as the node lists it. File and Symlink are nil when the node does not say.
type FileObject struct {
	Name     string `json:"name"`
	Created  string `json:"created"`
	Modified string `json:"modified"`
	Mode     string `json:"mode"`
	ModeBits string `json:"mode_bits"`
	Size     int64  `json:"size"`
	File     *bool  `json:"file"`
	Symlink  *bool  `json:"symlink"`
	Mime     string `json:"mime"`
}

// RenameItem moves a file inside a directory.
type RenameItem struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ChmodItem sets the mode of a file, such as "644".
type ChmodItem struct {
	File string `json:"file"`
	Mode string `json:"mode"`
}

// PullOptions asks the node to download a file from the internet into a server. A nil option is left to the node's default.
type PullOptions struct {
	URL        string
	Root       string
	FileName   string
	UseHeader  *bool
	Foreground *bool
}

func filesPath(serverUUID, operation string) string {
	return "/api/servers/" + serverUUID + "/files/" + operation
}

// ListDirectory lists a directory of a server.
func (c *Client) ListDirectory(ctx context.Context, serverUUID, directory string) ([]FileObject, error) {
	if err := checkUUID("the server", serverUUID); err != nil {
		return nil, err
	}
	var out []FileObject
	q := url.Values{"directory": {directory}}
	if err := c.run(ctx, call{method: http.MethodGet, path: filesPath(serverUUID, "list-directory") + "?" + q.Encode(), out: &out}); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadFile returns the content of a file, or ErrTooLarge if it is longer than max bytes (the editor of the panel takes no more).
func (c *Client) ReadFile(ctx context.Context, serverUUID, path string, max int64) ([]byte, error) {
	if err := checkUUID("the server", serverUUID); err != nil {
		return nil, err
	}
	var raw []byte
	q := url.Values{"file": {path}}
	if err := c.run(ctx, call{method: http.MethodGet, path: filesPath(serverUUID, "contents") + "?" + q.Encode(), raw: &raw, limit: max}); err != nil {
		return nil, err
	}
	return raw, nil
}

// WriteFile replaces the content of a file, or makes it.
func (c *Client) WriteFile(ctx context.Context, serverUUID, path string, content []byte) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	q := url.Values{"file": {path}}
	return c.run(ctx, call{method: http.MethodPost, path: filesPath(serverUUID, "write") + "?" + q.Encode(), body: append([]byte{}, content...), contentType: "text/plain"})
}

// CreateDirectory makes a directory called name inside path.
func (c *Client) CreateDirectory(ctx context.Context, serverUUID, name, path string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, filesPath(serverUUID, "create-directory"), map[string]string{"name": name, "path": path}, nil)
}

// RenameFiles moves files inside root.
func (c *Client) RenameFiles(ctx context.Context, serverUUID, root string, items []RenameItem) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPut, filesPath(serverUUID, "rename"), map[string]any{"root": root, "files": items}, nil)
}

// CopyFile copies a file next to itself.
func (c *Client) CopyFile(ctx context.Context, serverUUID, location string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, filesPath(serverUUID, "copy"), map[string]string{"location": location}, nil)
}

// DeleteFiles deletes files and directories inside root.
func (c *Client) DeleteFiles(ctx context.Context, serverUUID, root string, files []string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, filesPath(serverUUID, "delete"), map[string]any{"root": root, "files": files}, nil)
}

// CompressFiles packs files inside root into an archive and returns the archive's entry. It may take as long as the factory's
// LongTimeout.
func (c *Client) CompressFiles(ctx context.Context, serverUUID, root string, files []string) (FileObject, error) {
	if err := checkUUID("the server", serverUUID); err != nil {
		return FileObject{}, err
	}
	var out FileObject
	body, err := jsonBody(map[string]any{"root": root, "files": files})
	if err != nil {
		return FileObject{}, err
	}
	if err := c.run(ctx, call{method: http.MethodPost, path: filesPath(serverUUID, "compress"), body: body, contentType: "application/json", out: &out, long: true}); err != nil {
		return FileObject{}, err
	}
	return out, nil
}

// DecompressFile unpacks an archive inside root. It may take as long as the factory's LongTimeout.
func (c *Client) DecompressFile(ctx context.Context, serverUUID, root, file string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	body, err := jsonBody(map[string]any{"root": root, "file": file})
	if err != nil {
		return err
	}
	return c.run(ctx, call{method: http.MethodPost, path: filesPath(serverUUID, "decompress"), body: body, contentType: "application/json", long: true})
}

// ChmodFiles sets the modes of files inside root.
func (c *Client) ChmodFiles(ctx context.Context, serverUUID, root string, items []ChmodItem) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, filesPath(serverUUID, "chmod"), map[string]any{"root": root, "files": items}, nil)
}

// PullFile asks the node to download a file into a server.
func (c *Client) PullFile(ctx context.Context, serverUUID string, o PullOptions) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	body := map[string]any{"url": o.URL, "root": o.Root}
	if o.FileName != "" {
		body["file_name"] = o.FileName
	}
	if o.UseHeader != nil {
		body["use_header"] = *o.UseHeader
	}
	if o.Foreground != nil {
		body["foreground"] = *o.Foreground
	}
	return c.do(ctx, http.MethodPost, filesPath(serverUUID, "pull"), body, nil)
}
