# xwebfm

A web-based file manager for a Linux server. It runs as a privileged user
(normally root) so a single administrator can manage files owned by many
users, and exposes lower-level operations such as changing ownership and
reflink copies.

> **Security:** xwebfm has no authentication of its own and is intended to
> run as root. Always put it behind an authenticating reverse proxy (or
> similar) and keep it bound to localhost. Anyone who can reach it has root
> access to the exposed filesystem.

## Features

- Two-pane layout (Midnight Commander style), each browsing its own directory
- Multi-select with click, Ctrl/Cmd-click, Shift-click, Shift+arrows,
  Space/Insert and Ctrl+A
- Drag and drop between panes and onto folders (a drag within a pane moves,
  between panes copies; Ctrl/Alt forces copy, Shift forces move). Dropping
  files from the desktop uploads them
- Recursive copy and move with an ownership policy:
  - **keep**: keep the source's owner
  - **inherit**: take the owner of the destination folder (the default when
    it's owned by someone else, e.g. handing files to another user)
  - **set**: an explicit user and/or group
- Reflink control for copies (`auto`, `always`, `never`); moves are atomic
  renames within a filesystem and fall back to copy + delete across them
- Rename, new folder, delete, upload, and download (folders as `.tar.gz`)
- `chown` and `chmod`, optionally recursive
- Archive extraction (zip, 7z, rar including multi-volume, tar, tar.gz/tgz,
  tar.bz2, tar.xz, tar.zst, and plain .gz/.bz2/.xz/.zst) with optional
  passwords for 7z/rar, and archive creation (zip, tar, tar.gz, tar.bz2,
  tar.xz, tar.zst, plus 7z when the 7-Zip binary is installed). Both can
  delete the originals once they succeed
- A background job queue for copy, move, delete and recursive chown/chmod,
  with live progress (bytes, items, speed, time left) and cancel, pushed to
  the browser over a WebSocket. Panes refresh automatically when a job or
  another browser tab changes a directory they show

When ownership changes on copy, setuid/setgid bits are cleared from regular
files so a copy never becomes a setuid binary for its new owner.

### Keyboard

| Key | Action |
| --- | --- |
| Tab | Switch pane |
| ↑ ↓ PgUp PgDn Home End | Move cursor (Shift extends the selection) |
| Enter / Backspace | Open / go up |
| Space, Insert | Toggle selection of cursor row |
| Ctrl+A / Esc | Select all / clear selection |
| F2 F3 F5 F6 F7 F8 | Rename, download, copy, move, new folder, delete |

## Archives

Extraction uses pure-Go readers and writes every entry through directory
file descriptors, so an archive can never write outside its extraction:

- Entries are unpacked into a hidden temporary folder in the destination.
  If the archive holds a single top-level item it is moved into the
  destination as is; otherwise the folder is renamed after the archive
  (`photos.zip` → `photos/`, or `photos (2)/` if that exists). A failed or
  cancelled extraction leaves nothing behind.
- Absolute and `..` entry names are skipped, no path is ever written through
  a symlink (including symlinks the archive itself creates), hard links may
  only point at files inside the same extraction, device nodes/FIFOs are
  skipped and setuid/setgid bits are cleared. Skipped entries are reported.
- New files inherit the destination folder's owner by default; "keep" uses
  the owner recorded in tar headers.
- For multi-volume archives (`name.part1.rar`, `name.r00`, `name.7z.001`,
  `name.zip.001`) select any volume; "delete originals" removes every volume
  that was read.

Archives are written to a temporary file and renamed into place; originals
are deleted only after that succeeds. The Go writers read sources through
file descriptors like every other operation. 7z archives are created by the
`7zz`/`7z`/`7za` binary (detected at startup) with `-snl`, so symlinks are
stored rather than followed, but 7-Zip walks subdirectories by path itself.

## Jobs

Long operations run as jobs: `-jobs N` (default 2) run at once and the rest
wait in a FIFO queue. Jobs live in memory only, so a restart drops queued
jobs and history (the last 100 finished jobs are kept).

Cancelling a copy removes the partially created destination. Cancelling a
move between filesystems leaves the source intact (a same-filesystem move is
an atomic rename and finishes instantly). A cancelled delete or chown/chmod
stops part-way, and changes already made remain.

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/xwebfm` | Entry point: flags, HTTP server, graceful shutdown |
| `internal/fsops` | Filesystem operations and archive reading/writing, independent of HTTP |
| `internal/jobs` | In-memory job queue, workers, progress and event fan-out |
| `internal/api` | JSON API and WebSocket over `fsops` and `jobs` |
| `web/` | Svelte 5 + TypeScript SPA (Vite); `web/dist` is embedded into the binary |

## Path safety

All paths are relative to `-root` and resolved with `openat2(RESOLVE_IN_ROOT)`,
so `..` and symlinks can never escape it. Operations never follow a symlink
in the final path component (chown changes the link itself; chmod refuses
it), and recursive operations walk the tree by file descriptor with
`O_NOFOLLOW`, so a user can't redirect a privileged operation by swapping a
directory for a symlink mid-walk. Requires Linux 5.6+.

## Building

Requires Go 1.24+ and Node 22+.

```sh
make build        # builds web/dist, then the ./xwebfm binary
sudo ./xwebfm -listen 127.0.0.1:8080 -root / -jobs 2
```

The WebSocket only accepts same-origin connections (Origin must match the
Host header). If your reverse proxy rewrites `Host`, either forward it
(e.g. nginx `proxy_set_header Host $host;`, plus the usual `Upgrade` and
`Connection` headers) or allow the public host with `-origin files.example.com`.

For development, `make dev` runs the Go server on `:8080` and the Vite dev
server (which proxies `/api`) together. `make test` and `make lint` run the
checks; run `sudo go test ./internal/...` to cover ownership changes.

## API

All endpoints return JSON; errors look like
`{"error": {"code": "not_found", "message": "..."}}`. POST requests must use
`Content-Type: application/json` (uploads instead send an `X-Requested-With`
header). `dst` is always the full destination path. "owner…" means
`"owner": "keep|inherit|set"` plus `uid`/`user` and `gid`/`group` for `set`.

| Method | Path | Body / query |
| --- | --- | --- |
| GET | `/api/v1/fs/list` | `?path=/dir` |
| GET | `/api/v1/fs/stat` | `?path=/file` |
| GET | `/api/v1/fs/download` | `?path=` (files support ranges; directories stream as `.tar.gz`) |
| POST | `/api/v1/fs/chown` | `{"path", "uid" or "user", "gid" or "group", "recursive"}` |
| POST | `/api/v1/fs/chmod` | `{"path", "mode": "0755", "recursive"}` |
| POST | `/api/v1/fs/copy` | `{"src", "dst", "reflink": "auto\|always\|never", "overwrite", owner…}` → counts of files, dirs, reflinked, bytes |
| POST | `/api/v1/fs/move` | `{"src", "dst", "overwrite", owner…}` |
| POST | `/api/v1/fs/rename` | `{"path", "name"}` |
| POST | `/api/v1/fs/mkdir` | `{"path", "mode", owner…}` (owner defaults to `inherit`) |
| POST | `/api/v1/fs/delete` | `{"path"}` (recursive) |
| POST | `/api/v1/fs/upload` | multipart; `?dir=&overwrite=&owner=` (owner defaults to `inherit`) |
| GET | `/api/v1/users`, `/api/v1/groups` | |
| POST | `/api/v1/jobs` | `{"kind": "copy\|move", "items": [{"src", "dst"}], …}` or `{"kind": "delete\|chown\|chmod", "paths": [...], …}` with the same options as the synchronous endpoints; `{"kind": "extract", "items": [{"src": archive, "dst": dir}], "password", "deleteOriginals", owner…}`; `{"kind": "archive", "paths": [...], "dst": file, "format", "overwrite", "deleteOriginals", owner…}` → 202 + job |
| GET | `/api/v1/jobs` | all jobs |
| GET | `/api/v1/info` | capabilities, e.g. `{"archive": {"create": ["zip", …, "7z"]}}` |
| POST | `/api/v1/jobs/{id}/cancel`, `/api/v1/jobs/{id}/dismiss`, `/api/v1/jobs/clear` | |
| GET | `/api/v1/ws` | WebSocket event stream (see below) |
| GET | `/healthz` | |

The synchronous `/api/v1/fs/*` endpoints remain for scripting; closing the
connection cancels them.

### WebSocket events

`/api/v1/ws` sends `{"type": "snapshot", "jobs": [...]}` first, then:

- `{"type": "job", "job": {...}}` when a job is queued, starts, finishes, or
  its progress changes (at most every 250 ms)
- `{"type": "removed", "id": "..."}` when a job is dismissed or pruned
- `{"type": "changed", "dirs": [...]}` when directories changed (finished
  jobs and synchronous operations)

A job has `id, kind, title, state` (`queued`, `running`, `done`, `failed`,
`cancelled`), `items`, timestamps, `progress {totalEntries, totalBytes,
entries, bytes, current}`, `errors` and `dirs`. A client that falls too far
behind is disconnected and should reconnect for a fresh snapshot.

