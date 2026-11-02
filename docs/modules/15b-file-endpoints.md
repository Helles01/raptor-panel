# M15 – the file manager routes

Status: **handlers done over storage interfaces** (2026-10-26): `internal/api/fileapi` and `internal/wings/files.go`. The rules are
in `internal/files` (15a).

## Routes: `/api/client/servers/{server}/files`

| Route | Permission | What it does |
|---|---|---|
| `GET /list?directory=` | `file.read` | `file_object` list; `is_file` defaults to true, `mimetype` to `application/octet-stream`, times are Atom in the panel's zone |
| `GET /contents?file=` | `file.read-content` | the text of the file for the editor, up to 4 MiB; longer is 400 "too large to view in the file editor"; served as `text/plain` with `nosniff` |
| `GET /download?file=` | `file.read-content` | `signed_url` to the node with a 15-minute token for that one file |
| `GET /upload` | `file.create` | `signed_url` for uploading, 15 minutes |
| `POST /write?file=` | `file.create` | the body is the new content, at most the editor limit (413 above it) |
| `POST /create-folder` | `file.create` | `root`, `name` |
| `PUT /rename` | `file.update` | `root`, `files` [{from, to}] |
| `POST /copy` | `file.create` | `location` |
| `POST /compress` | `file.archive` | `root`, `files`; answers with the archive's `file_object` |
| `POST /decompress` | `file.create` | `root`, `file` |
| `POST /delete` | `file.delete` | `root`, `files` |
| `POST /chmod` | `file.update` | `root`, `files` [{file, mode}]; the mode may be sent as text or as a number; no entry is written (as in the reference) |
| `POST /pull` | `file.create` | `url`, `directory`, `filename`, `use_header`, `foreground`; 5 per 10 minutes per server, counted after the authorizer (C-058) |

The permissions come from one table, `files.PermissionFor`, and every route is registered with it. Two quirks of the reference are
kept on purpose and noted in 15a: writing and decompressing need `file.create` (so a holder of `file.create` can overwrite an
existing file), rename and chmod need `file.update`.

Entries: `server:file.read|download|write|create-directory|rename|copy|compress|decompress|delete|pull`. A pull's `url` is logged
**without** its user name, query and fragment (C-062).

## Validation
Every field is read with the same allow-lists as 15a (C-030): paths are text without control characters up to 4096 bytes, at most
1000 files per request, a mode is three or four octal digits, a new folder or pulled file has a plain name, a pull URL is http(s) with
a host and no credentials. A refusal names the field (`directory`, `file`, `files`, `name`, `url`, `filename`, …). Bodies are read
with a 1 MiB limit.

## Wings (`internal/wings/files.go`)
`ListDirectory`, `ReadFile(max)` (stops at the limit; `ErrTooLarge`), `WriteFile`, `CreateDirectory`, `RenameFiles`, `CopyFile`,
`DeleteFiles`, `CompressFiles`, `DecompressFile`, `ChmodFiles`, `PullFile`. Packing and unpacking can take minutes, so they use a
second transport with its own timeout (`ClientOptions.LongTimeout`, default 15 minutes) and the ordinary calls keep the short one.

## Different from the reference (`docs/compat.md`)
- C-030: the path of a download is not decoded a second time.
- **C-062**: a write is capped at the editor limit; a pull's address is logged without its secrets; `contents` also carries
  `nosniff`; the pull throttle counts only authorized calls.

## Tests
99 % statement coverage of `fileapi`; killed mutants include every default (root, mime, file/symlink flags, time zone), the editor
limit boundaries, the token and no-store headers, the redaction, the throttle key, each permission of the table, and the order
validation → node → entry (a call that fails writes no entry).

## Still to do
The target store (the server's node with its token opened), integration tests against a real Wings, the upload through the node
(the browser talks to the node directly; the panel only signs).
