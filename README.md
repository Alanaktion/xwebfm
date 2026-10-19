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

## Layout

| Path | Purpose |
| --- | --- |
| `cmd/xwebfm` | Entry point: flags, HTTP server, graceful shutdown |
| `internal/fsops` | Filesystem operations, independent of HTTP |
| `internal/api` | JSON API over `fsops` |
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
sudo ./xwebfm -listen 127.0.0.1:8080 -root /
```

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
| GET | `/healthz` | |
