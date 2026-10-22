# M15 groundwork – file manager rules

Status: **groundwork done** (2026-10-22): the rules that need no node. The endpoints (`/servers/{server}/files/*`), the calls to
the node (`list-directory`, `contents`, `write`, `rename`, …), the upload and download tokens and the activity events wait for
the schema and for a real Wings.

## Done: `internal/files`
| Function | Content |
|---|---|
| `PermissionFor(op)` | The permission of each of the 13 operations, as one table. An unknown operation has none and is refused. A test reads the 12 request classes of the reference and compares each permission with ours. |
| `ValidatePath`, `ValidateName`, `Root` | Paths: text, not empty, at most 4096 bytes, no control characters. Names (new folder, pulled file): no `/`, not `.` or `..`, at most 255 bytes. No root means `/`. |
| `ValidateFileList`, `ValidateRename`, `ValidateChmod` | The body of delete and compress, rename, and chmod: 1 to 1000 items, every path checked, a chmod mode of three or four octal digits. |
| `ValidatePull`, `ValidatePullURL` | A pull request: an http(s) URL with a host, no credentials, no space, control character or backslash, at most 2048 characters; the optional directory and file name. |

Mutation checks confirmed that each of these fails a test: the table entry for write, unknown operations, the scheme, the
credentials, the backslash and space check, slashes in names, modes with 8 and 9, the empty root, the file-count boundary, control
characters, the port range and the empty host. 100 % of the statements are covered.

## What matches the reference
The permission of every operation (including the two oddities below), the default root, and that `..` in a rename target is
allowed. Everything the reference leaves to the node is still left to the node.

## Deliberate differences (`docs/compat.md`: C-030)
Input checks the reference does not have, and a download link that does not decode the path twice.

## Decision of the project owner (2026-10-22): keep the reference's table for now
The project owner decided to leave this as it is for the time being; it can be revisited (it would change one table entry and
add a check in the write handler).
In the reference, **saving a file needs `file.create`**, not `file.update` (`WriteFileContentRequest`), and decompressing needs
`file.create` too. The frontend shows the save button of an existing file only with `file.update`. So:
- a subuser with `file.create` but not `file.update` can **overwrite existing files** through the API, although the interface
  would not let them, and `rename` and `chmod` do need `file.update`;
- a subuser with `file.update` but not `file.create` sees a save button that the API refuses.

Raptor keeps the reference's table so existing roles behave the same. The tidy rule would be "overwriting an existing file needs
`file.update`, creating a new one needs `file.create`", but the panel cannot tell which case a write is (the node knows), so it
would need either both permissions for every write or a question to the node first. It changes who can do what, so it is not
done without a decision.

## Still to do (waits for the schema and a real Wings)
- The endpoints and the node calls; the upload token (`wings.Issuer.FileUpload` exists) and the download token (`FileDownload`,
  with the path unchanged); the activity events (`server:file.read|download|write|create-directory|rename|copy|compress|decompress|delete|pull`);
  the size limit for reading a file in the editor (the reference's `files.max_edit_size`).
- An end-to-end test on a real Wings that paths trying to leave the server's directory (`../../`, absolute paths, symbolic
  links) are refused by the node: the panel relies on that and does not duplicate it (M17).
