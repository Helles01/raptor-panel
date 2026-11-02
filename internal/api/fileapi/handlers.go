package fileapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/files"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// atom is how the reference writes a time (Carbon's toAtomString): the offset as +00:00, never Z.
const atom = "2006-01-02T15:04:05-07:00"

// maxBody is the largest JSON body these routes read: a thousand paths of 4096 bytes would be more, but nothing real is.
const maxBody = 1 << 20

type fileAttributes struct {
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	ModeBits   string `json:"mode_bits"`
	Size       int64  `json:"size"`
	IsFile     bool   `json:"is_file"`
	IsSymlink  bool   `json:"is_symlink"`
	Mimetype   string `json:"mimetype"`
	CreatedAt  string `json:"created_at"`
	ModifiedAt string `json:"modified_at"`
}

func (h *Handlers) when(s string) string {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// The reference reads a time it cannot read as the time of the request.
		t = h.d.Now()
	}
	return t.In(h.d.Location).Format(atom)
}

func (h *Handlers) item(o wings.FileObject) resp.Item {
	isFile, isSymlink := true, false
	if o.File != nil {
		isFile = *o.File
	}
	if o.Symlink != nil {
		isSymlink = *o.Symlink
	}
	mime := o.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	return resp.NewItem("file_object", fileAttributes{
		Name: o.Name, Mode: o.Mode, ModeBits: o.ModeBits, Size: o.Size, IsFile: isFile, IsSymlink: isSymlink, Mimetype: mime,
		CreatedAt: h.when(o.Created), ModifiedAt: h.when(o.Modified),
	})
}

// target finds the node of the server of the route.
func (h *Handlers) target(c echo.Context, p authz.Principal) (controlapi.Target, error) {
	t, err := h.d.Targets.For(c.Request().Context(), p.Server.ID)
	switch {
	case errors.Is(err, controlapi.ErrNotFound):
		return controlapi.Target{}, apierr.NotFound()
	case err != nil:
		return controlapi.Target{}, apierr.Internal(err)
	}
	return t, nil
}

// log writes an entry about the server of the route. A failure is logged and never reaches the person.
func (h *Handlers) log(c echo.Context, p authz.Principal, event string, values map[string]any) {
	props, err := activity.Properties(event, values)
	if err != nil {
		h.d.Logger.Error("activity entry refused", "event", event, "error", err)
		return
	}
	e := Entry{Event: event, ServerID: p.Server.ID, ActorID: p.User.ID, IP: c.RealIP(), Properties: props}
	if p.Key != nil {
		id := p.Key.ID
		e.APIKeyID = &id
	}
	if err := h.d.Activity.Log(c.Request().Context(), e); err != nil {
		h.d.Logger.Error("activity entry not stored", "event", event, "server", p.Server.ID, "error", err)
	}
}

// sentence makes the text of a rule into a sentence for the person: capital letter, full stop.
func sentence(err error) string {
	s := strings.TrimSpace(err.Error())
	r, size := utf8.DecodeRuneInString(s)
	s = strings.ToUpper(string(r)) + s[size:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// invalid is the 422 for one field.
func invalid(name string, err error) error {
	v := validate.New()
	v.Custom(name, "invalid", sentence(err))
	return v.Err()
}

// queryPath reads a path from the query. A required one that is missing is a 422.
func queryPath(c echo.Context, name string, required bool) (string, error) {
	value := c.QueryParam(name)
	if value == "" {
		if required {
			v := validate.New()
			v.Custom(name, "required", fmt.Sprintf("The %s field is required.", name))
			return "", v.Err()
		}
		return "", nil
	}
	if err := files.ValidatePath(value); err != nil {
		return "", invalid(name, err)
	}
	return value, nil
}

// decodeCall maps a failed call to a node to the API's answer: a file that is too large for the editor is a message to the person.
func decodeCall(err error) error {
	if errors.Is(err, wings.ErrTooLarge) {
		return apierr.Display("The file you are attempting to open is too large to view in the file editor.")
	}
	return controlapi.DaemonError(err)
}

// List is GET /files/list?directory=.
func (h *Handlers) List(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	dir, err := queryPath(c, "directory", false)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	list, err := h.d.Daemon.List(c.Request().Context(), t.Node, t.UUID, files.Root(dir))
	if err != nil {
		return decodeCall(err)
	}
	items := make([]resp.Item, len(list))
	for i, o := range list {
		items[i] = h.item(o)
	}
	return c.JSON(http.StatusOK, resp.NewList(items))
}

// Contents is GET /files/contents?file=: the text of a file, for the editor, up to MaxEditSize.
func (h *Handlers) Contents(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	file, err := queryPath(c, "file", true)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	content, err := h.d.Daemon.Read(c.Request().Context(), t.Node, t.UUID, file, h.d.MaxEditSize)
	if err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileRead, map[string]any{"file": file})
	// Plain text whatever the file is, and never to be taken for anything else: a file with HTML in it must not run in the panel's page.
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	return c.Blob(http.StatusOK, "text/plain", content)
}

// Download is GET /files/download?file=: the address on the node and a token for 15 minutes for that one file.
func (h *Handlers) Download(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	file, err := queryPath(c, "file", true)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	token, err := h.d.Tokens.FileDownload(t.Node, subject{id: p.User.ID, uuid: p.User.UUID, held: p.Permissions}, t.UUID, file)
	switch {
	case errors.Is(err, wings.ErrMissingPermission):
		return apierr.Forbidden("This action is unauthorized.")
	case err != nil:
		return apierr.Internal(err)
	}
	h.log(c, p, activity.ServerFileDownload, map[string]any{"file": file})
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"object": "signed_url", "attributes": map[string]string{"url": t.Node.FileDownloadURL(token)}})
}

// Upload is GET /files/upload: the address on the node that files are sent to and a token for 15 minutes.
func (h *Handlers) Upload(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	token, err := h.d.Tokens.FileUpload(t.Node, subject{id: p.User.ID, uuid: p.User.UUID, held: p.Permissions}, t.UUID)
	switch {
	case errors.Is(err, wings.ErrMissingPermission):
		return apierr.Forbidden("This action is unauthorized.")
	case err != nil:
		return apierr.Internal(err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, map[string]any{"object": "signed_url", "attributes": map[string]string{"url": t.Node.UploadURL(token)}})
}

// Write is POST /files/write?file=: the body is the new content of the file.
func (h *Handlers) Write(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	file, err := queryPath(c, "file", true)
	if err != nil {
		return err
	}
	content, err := io.ReadAll(io.LimitReader(c.Request().Body, h.d.MaxEditSize+1))
	if err != nil {
		return apierr.BadRequest("The request body could not be read.")
	}
	if int64(len(content)) > h.d.MaxEditSize {
		return apierr.TooLarge()
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Write(c.Request().Context(), t.Node, t.UUID, file, content); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileWrite, map[string]any{"file": file})
	return c.NoContent(http.StatusNoContent)
}

// textField reads an optional text of a body, which may be left out, null or blank.
func textField(s field.String, name string, v *validate.Validator) string {
	v.Type(name, s.State, validate.KindString, true)
	return s.V
}

// stringList reads a list of texts of a body. It is needed: absent, null and empty are all "required".
func stringList(raw json.RawMessage, name string, v *validate.Validator) []string {
	if len(raw) == 0 || string(raw) == "null" {
		v.Custom(name, "required", fmt.Sprintf("The %s field is required.", name))
		return nil
	}
	var elements []json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &elements) != nil {
		v.Custom(name, "array", fmt.Sprintf("The %s must be an array.", name))
		return nil
	}
	if len(elements) == 0 {
		v.Custom(name, "required", fmt.Sprintf("The %s field is required.", name))
		return nil
	}
	out := make([]string, 0, len(elements))
	for i, e := range elements {
		var s string
		if json.Unmarshal(e, &s) != nil {
			v.Custom(name, "string", fmt.Sprintf("The %s.%d must be a string.", name, i))
			return nil
		}
		out = append(out, s)
	}
	return out
}

type folderBody struct {
	Root field.String `json:"root"`
	Name field.String `json:"name"`
}

// CreateFolder is POST /files/create-folder.
func (h *Handlers) CreateFolder(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body folderBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	root := textField(body.Root, "root", v)
	v.Sent("name", body.Name.State)
	v.Type("name", body.Name.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := files.ValidatePath(files.Root(root)); err != nil {
		return invalid("root", err)
	}
	if err := files.ValidateName(body.Name.V); err != nil {
		return invalid("name", err)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.CreateDirectory(c.Request().Context(), t.Node, t.UUID, body.Name.V, files.Root(root)); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileCreateDirectory, map[string]any{"name": body.Name.V, "directory": files.Root(root)})
	return c.NoContent(http.StatusNoContent)
}

type renameBody struct {
	Root  field.String    `json:"root"`
	Files json.RawMessage `json:"files"`
}

// Rename is PUT /files/rename: moves or renames files inside a directory.
func (h *Handlers) Rename(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body renameBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	root := textField(body.Root, "root", v)
	items := renameItems(body.Files, v)
	if err := v.Err(); err != nil {
		return err
	}
	ruleItems := make([]files.Rename, len(items))
	for i, it := range items {
		ruleItems[i] = files.Rename{From: it.From, To: it.To}
	}
	if err := files.ValidateRename(root, ruleItems); err != nil {
		return invalid("files", err)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Rename(c.Request().Context(), t.Node, t.UUID, files.Root(root), items); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileRename, map[string]any{"directory": files.Root(root), "files": items})
	return c.NoContent(http.StatusNoContent)
}

func renameItems(raw json.RawMessage, v *validate.Validator) []wings.RenameItem {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		v.Custom("files", "required", "The files field is required.")
		return nil
	}
	var list []map[string]json.RawMessage
	if raw[0] != '[' || json.Unmarshal(raw, &list) != nil {
		v.Custom("files", "array", "The files must be an array.")
		return nil
	}
	out := make([]wings.RenameItem, 0, len(list))
	for i, e := range list {
		var from, to string
		if json.Unmarshal(e["from"], &from) != nil || from == "" {
			v.Custom("files", "required", fmt.Sprintf("The files.%d.from field is required.", i))
			return nil
		}
		if json.Unmarshal(e["to"], &to) != nil || to == "" {
			v.Custom("files", "required", fmt.Sprintf("The files.%d.to field is required.", i))
			return nil
		}
		out = append(out, wings.RenameItem{From: from, To: to})
	}
	return out
}

type copyBody struct {
	Location field.String `json:"location"`
}

// Copy is POST /files/copy: a copy of a file next to it.
func (h *Handlers) Copy(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body copyBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("location", body.Location.State)
	v.Type("location", body.Location.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := files.ValidatePath(body.Location.V); err != nil {
		return invalid("location", err)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Copy(c.Request().Context(), t.Node, t.UUID, body.Location.V); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileCopy, map[string]any{"file": body.Location.V})
	return c.NoContent(http.StatusNoContent)
}

type listBody struct {
	Root  field.String    `json:"root"`
	Files json.RawMessage `json:"files"`
}

// readList reads the root and the files of a delete or compress request, and checks them.
func readList(c echo.Context) (root string, names []string, err error) {
	var body listBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return "", nil, err
	}
	v := validate.New()
	root = textField(body.Root, "root", v)
	names = stringList(body.Files, "files", v)
	if err := v.Err(); err != nil {
		return "", nil, err
	}
	if err := files.ValidateFileList(root, names); err != nil {
		return "", nil, invalid("files", err)
	}
	return files.Root(root), names, nil
}

// Compress is POST /files/compress: packs files into an archive next to them and answers with the archive's entry.
func (h *Handlers) Compress(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	root, names, err := readList(c)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	archive, err := h.d.Daemon.Compress(c.Request().Context(), t.Node, t.UUID, root, names)
	if err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileCompress, map[string]any{"directory": root, "files": names})
	return c.JSON(http.StatusOK, h.item(archive))
}

// Delete is POST /files/delete.
func (h *Handlers) Delete(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	root, names, err := readList(c)
	if err != nil {
		return err
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Delete(c.Request().Context(), t.Node, t.UUID, root, names); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileDelete, map[string]any{"directory": root, "files": names})
	return c.NoContent(http.StatusNoContent)
}

type decompressBody struct {
	Root field.String `json:"root"`
	File field.String `json:"file"`
}

// Decompress is POST /files/decompress: unpacks an archive.
func (h *Handlers) Decompress(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body decompressBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	root := textField(body.Root, "root", v)
	v.Sent("file", body.File.State)
	v.Type("file", body.File.State, validate.KindString, false)
	if err := v.Err(); err != nil {
		return err
	}
	if err := files.ValidatePath(files.Root(root)); err != nil {
		return invalid("root", err)
	}
	if err := files.ValidatePath(body.File.V); err != nil {
		return invalid("file", err)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Decompress(c.Request().Context(), t.Node, t.UUID, files.Root(root), body.File.V); err != nil {
		return decodeCall(err)
	}
	h.log(c, p, activity.ServerFileDecompress, map[string]any{"directory": files.Root(root), "files": body.File.V})
	return c.NoContent(http.StatusNoContent)
}

type chmodBody struct {
	Root  field.String    `json:"root"`
	Files json.RawMessage `json:"files"`
}

// modeText reads a mode, which a client may send as a number (644) or as text ("644").
func modeText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if _, err := strconv.ParseUint(n.String(), 10, 32); err == nil {
			return n.String(), true
		}
	}
	return "", false
}

// Chmod is POST /files/chmod: sets the modes of files.
func (h *Handlers) Chmod(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	var body chmodBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	root := textField(body.Root, "root", v)
	var items []wings.ChmodItem
	switch {
	case len(body.Files) == 0 || string(body.Files) == "null" || string(body.Files) == "[]":
		v.Custom("files", "required", "The files field is required.")
	default:
		var list []map[string]json.RawMessage
		if body.Files[0] != '[' || json.Unmarshal(body.Files, &list) != nil {
			v.Custom("files", "array", "The files must be an array.")
			break
		}
		for i, e := range list {
			var file string
			if json.Unmarshal(e["file"], &file) != nil || file == "" {
				v.Custom("files", "required", fmt.Sprintf("The files.%d.file field is required.", i))
				break
			}
			mode, ok := modeText(e["mode"])
			if !ok {
				v.Custom("files", "numeric", fmt.Sprintf("The files.%d.mode must be a number.", i))
				break
			}
			items = append(items, wings.ChmodItem{File: file, Mode: mode})
		}
	}
	if err := v.Err(); err != nil {
		return err
	}
	ruleItems := make([]files.Chmod, len(items))
	for i, it := range items {
		ruleItems[i] = files.Chmod{File: it.File, Mode: it.Mode}
	}
	if err := files.ValidateChmod(root, ruleItems); err != nil {
		return invalid("files", err)
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Chmod(c.Request().Context(), t.Node, t.UUID, files.Root(root), items); err != nil {
		return decodeCall(err)
	}
	return c.NoContent(http.StatusNoContent)
}

type pullBody struct {
	URL        field.String `json:"url"`
	Directory  field.String `json:"directory"`
	Filename   field.String `json:"filename"`
	UseHeader  field.Bool   `json:"use_header"`
	Foreground field.Bool   `json:"foreground"`
}

// Pull is POST /files/pull: the node downloads a file from the internet.
func (h *Handlers) Pull(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	// Five in ten minutes for a server, whoever pulls. Only calls that passed the authorizer count.
	if allowed, retry := h.d.PullLimiter.Allow(p.Server.UUID); !allowed {
		return apierr.TooManyRequests(retry)
	}
	var body pullBody
	if err := validate.DecodeJSON(c.Request().Body, &body, maxBody); err != nil {
		return err
	}
	v := validate.New()
	v.Sent("url", body.URL.State)
	v.Type("url", body.URL.State, validate.KindString, false)
	dir := textField(body.Directory, "directory", v)
	name := textField(body.Filename, "filename", v)
	v.Type("use_header", body.UseHeader.State, validate.KindBoolean, true)
	v.Type("foreground", body.Foreground.State, validate.KindBoolean, true)
	if err := v.Err(); err != nil {
		return err
	}
	// One field at a time, so that the refusal names the right one.
	if err := files.ValidatePullURL(body.URL.V); err != nil {
		return invalid("url", err)
	}
	if dir != "" {
		if err := files.ValidatePath(dir); err != nil {
			return invalid("directory", err)
		}
	}
	if name != "" {
		if err := files.ValidateName(name); err != nil {
			return invalid("filename", err)
		}
	}

	opts := wings.PullOptions{URL: body.URL.V, Root: files.Root(dir), FileName: name}
	if body.UseHeader.State == field.Valid {
		opts.UseHeader = &body.UseHeader.V
	}
	if body.Foreground.State == field.Valid {
		opts.Foreground = &body.Foreground.V
	}
	t, err := h.target(c, p)
	if err != nil {
		return err
	}
	if err := h.d.Daemon.Pull(c.Request().Context(), t.Node, t.UUID, opts); err != nil {
		return decodeCall(err)
	}
	// The address is logged without what it carries in its user name, query and fragment: it is shown to everybody who reads the log.
	h.log(c, p, activity.ServerFilePull, map[string]any{"directory": files.Root(dir), "url": activity.RedactURL(body.URL.V)})
	return c.NoContent(http.StatusNoContent)
}
