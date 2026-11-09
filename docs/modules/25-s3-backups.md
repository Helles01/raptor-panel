# 25 — Backups kept on S3 (closes C-060)

Written 2026-11-08, at the owner's word ("nem tudom volt-e, de ha hasznos, építsd be"): the old panel may have kept backups on S3, and a Raptor that cannot reach them would
leave them in the bucket with no way to download, restore or delete them. Until now only the node's own disk (`wings`) was handled; a backup on S3 was listed and refused.

## What the owner sets
All from the environment (or the git-ignored configuration file), never from the database:

| Setting | Env | Default | Meaning |
|---|---|---|---|
| `backups.driver` | `RAPTOR_BACKUPS_DRIVER` | `wings` | where **new** backups go: `wings` or `s3`. Backups that exist keep the disk they were made on, so this can be changed later (the reference says the same) |
| `backups.s3.endpoint` | `RAPTOR_BACKUPS_S3_ENDPOINT` | (Amazon S3) | the address of an S3-compatible server (MinIO and the like), `http(s)://host[:port]`, no path |
| `backups.s3.region` | `RAPTOR_BACKUPS_S3_REGION` | | required; also the region of the signature |
| `backups.s3.bucket` | `RAPTOR_BACKUPS_S3_BUCKET` | | required |
| `backups.s3.access_key`, `secret_key` | `RAPTOR_BACKUPS_S3_ACCESS_KEY`, `…_SECRET_KEY` | | required; never printed, logged or put in an error |
| `backups.s3.path_style` | `RAPTOR_BACKUPS_S3_PATH_STYLE` | `false` | `https://host/bucket/key` instead of `https://bucket.host/key`; most S3-compatible servers want it |
| `backups.s3.storage_class` | `RAPTOR_BACKUPS_S3_STORAGE_CLASS` | | for example `STANDARD_IA` |
| `backups.presigned_minutes` | `RAPTOR_BACKUPS_PRESIGNED_MINUTES` | 60 | how long the addresses the node uploads the parts to are good for (1 to 10080) |
| `backups.max_part_size` | `RAPTOR_BACKUPS_MAX_PART_SIZE` | 5 GiB | the size of one part (5 MiB to 5 GiB, S3's own limits); 10000 parts at most, so a 5 MiB part size limits an archive to about 48 GiB |

Giving any S3 setting makes S3 available for the backups that are on it, **whichever driver is set**; a panel with `driver: wings` and a bucket still downloads, restores and deletes the old
backups on S3. All of it is checked at start: a wrong or half-given bucket stops the panel with the reason, and `driver: s3` without a bucket is refused. A panel with no S3 settings behaves as before:
a backup on S3 is listed, and download, restore and delete refuse it with a 400.

## How it works
- **`internal/s3`**: the part of the S3 API that is needed, with AWS Signature Version 4 written with the standard library (no new dependency): `CreateMultipartUpload`, signed `UploadPart` and `GetObject`
  addresses, `CompleteMultipartUpload` (an error inside a `200` is an error), `ListParts` (paged), `AbortMultipartUpload`, `DeleteObject`, `Head`. Redirects are not followed (a signed request cannot go to another
  host); answers are cut to 1 MiB; an error carries the server's code and message with control characters removed, never a signed address.
- **The signature is checked against the AWS documentation's own examples**, which have a known answer: the signed `GET` address (`aeeed9bb…`), the `GET Object` with a range (`f0e8bdb8…`), the `PUT Object` with a
  body and a `$` in the key (`98ad7217…`) and the `GET Bucket` with a query (`34b48302…`). A request sent to a test server is verified again from what arrived, so what is signed is what goes on the wire.
- **`internal/app/adapters.go` (`s3Backups`)** is the bucket side of both `backupapi.Archives` and `remoteapi.Uploads`. The key of an archive is the reference's, `<server uuid>/<backup uuid>.tar.gz`, so the backups an
  old panel put in the bucket are found where they were put.

### The life of a backup on S3
1. **Start** (`POST /backups`, or a schedule): the row gets the disk of the driver, and the node is told `adapter: s3`. A schedule that replaces the oldest backup removes its archive from where it is, the
   bucket for one on S3 and the node for one there.
2. **Upload addresses** (`GET /api/remote/backups/{backup}?size=`, the node): the panel starts the multipart upload, keeps its id on the backup (`backups.upload_id`, only while the backup has not ended),
   and answers `parts` (one address for each `ceil(size / part_size)`, good for `presigned_minutes`) and `part_size`. More than 10000 parts is a 400.
3. **The end** (`POST /api/remote/backups/{backup}`): a successful report with `parts` (`[{"etag", "part_number"}]`, at most 10000, validated) puts the parts together; without `parts` the bucket is asked which
   there are. A failed report aborts the upload. The checks of the reference's report (a backup that ended cannot be reported on again, C-026) come first.
4. **Download** (`GET …/download`): a signed address of the bucket, good for 5 minutes, answered as the reference answers it (`signed_url`); nothing is asked of the node.
5. **Restore** (`POST …/restore`): the node is given the same kind of address in `download_url`, with `adapter: s3`; an address that cannot be made stops the restore before the server is marked as restoring.
6. **Delete**: the object goes first and then the row, so a bucket that refuses leaves the backup listed and deletable again.

## Different from the reference (C-060 in `docs/compat.md`)
- **The backup's own disk decides**, not the panel's current default: the reference decides on the default adapter when a node reports, which would try to finish an upload that was never on S3 after the default is changed.
- **The upload is finished before the end is stored**, not inside the database transaction: S3 can take minutes to put a big archive together, and a refusal leaves the backup unfinished, so the node reports again.
  A repeated report finds the upload gone (`NoSuchUpload`) and the object there with the size the node reported: that counts as finished.
- Left alone, as in the reference: objects of deleted servers and of backups the prune job marked as failed (the bucket's lifecycle rules clear parts that were never finished).

## Tests
- `internal/s3`: the four documentation signatures, escaping, validation of every setting, keys never printed, addresses of the four address styles, every request against a test server that checks the signature
  from what arrived, the 200-with-an-error answer, paging, redirects, errors.
- `remoteapi`, `backupapi`: the whole of 1 to 6 with fakes, including a storage that refuses (nothing is stored, the node can report again), a failed upload (aborted), a backup that ended meanwhile (the upload is
  thrown away), parts that are not parts, a node that is not the backup's, and a panel without S3 settings.
- `internal/app`: the adapter against a test bucket (part counts, the limit of parts, finishing with and without the list of parts, an upload that was already finished).
- On the real MariaDB: `upload_id` kept only while the backup has not ended, ten at once.
- **`TestPanelBackupsOnS3`**: the panel that is running, a test bucket, the node's token: addresses for three parts, the upload put together on the report, a second report that does nothing, the download address
  (and the bucket serving it), the delete; neither a key nor a signed address is in the panel's log.
- 15 mutants made to fail on purpose (the upload never finished, an error ignored, no abort, no delete from the bucket, no address for a restore, new backups always on the node, parts rounded down, an upload that is
  gone always a success, another key, unsorted query, wrong signing key, a slash in a query value, the check of the settings skipped, a key printed, redirects followed): all were caught.

## Not tested here
A real bucket: there is none on this machine. The signature is verified against the AWS examples and the requests against a test server that follows the documented S3 behaviour, but the first run against a real
S3 or MinIO is for the VPS test (`docs/first-test.md`): set the `RAPTOR_BACKUPS_*` variables, start a backup from the panel and watch the object appear.
