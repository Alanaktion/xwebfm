# xwebfm

A web-based file manager for a Linux server. It runs as a privileged user
(normally root) so a single administrator can manage files owned by many
users, and exposes lower-level operations such as changing ownership and
reflink copies.

> **Security:** xwebfm has no authentication of its own and is intended to
> run as root. Always put it behind an authenticating reverse proxy (or
> similar) and keep it bound to localhost. Anyone who can reach it has root
> access to the exposed filesystem.

## Features (so far)

- Browse directories with type, size, mode, owner/group and mtime
- `chown` by name or ID, optionally recursive
- `chmod` with an octal / checkbox editor, optionally recursive
- Copy files with reflink control (`auto`, `always`, `never`) and optional
  preservation of owner, mode and timestamps

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
`Content-Type: application/json`.

| Method | Path | Body / query |
| --- | --- | --- |
| GET | `/api/v1/fs/list` | `?path=/dir` |
| GET | `/api/v1/fs/stat` | `?path=/file` |
| POST | `/api/v1/fs/chown` | `{"path", "uid" or "user", "gid" or "group", "recursive"}` |
| POST | `/api/v1/fs/chmod` | `{"path", "mode": "0755", "recursive"}` |
| POST | `/api/v1/fs/copy` | `{"src", "dst", "reflink": "auto\|always\|never", "preserve", "overwrite"}` → `{"reflinked", "bytes", "entry"}` |
| GET | `/api/v1/users`, `/api/v1/groups` | |
| GET | `/healthz` | |
