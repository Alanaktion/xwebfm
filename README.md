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
- **Find & disk usage**: an ncdu-style usage view with drill-down and a
  per-owner breakdown (stays on one filesystem, hard links counted once);
  search by name, type, owner, size and age, with presets for files whose
  owner/group no longer exists, files not owned by the folder's owner,
  world-writable and setuid/setgid files; free space per pane
- **Permission helpers**: "fix owner" (recursively give a tree its own or
  its parent's owner), chmod presets with separate folder/file modes
  (755/644, 2775/664 …), owner cells highlighted when they differ from the
  folder's owner or have no account, POSIX ACL editor (named entries and
  default ACLs, optionally recursive), inode attributes (immutable,
  append-only, no-COW …), and symlink/hard link creation
- **Navigation & viewing**: type a path (Tab completes), places sidebar with
  user homes and bookmarks, type-to-filter, sortable columns, hidden-file
  toggle, a viewer for text/images/audio/video/hex, an editor for text files,
  and a compare mode that highlights differences between the two panes

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
| F2 F3 F4 F5 F6 F7 F8 | Rename, view, edit, copy, move, new folder, delete |
| Shift+F3 | Download |
| Alt+Enter | Properties (ACL editor, attributes) |
| Alt+F7 / Ctrl+U | Find / disk usage |
| Ctrl+L | Type a path (Tab completes folder names) |
| Letters, `/` | Filter the pane by name (Esc clears) |
| Ctrl+H / Ctrl+= | Toggle hidden files / compare panes |
| Ctrl+/ or ? | Keyboard shortcut guide |

Which columns the listings show (Size, On disk, Mode, Owner, Modified) is
chosen with the Columns button or by right-clicking a column header, and
remembered in the browser.

### Viewing and editing

Inline views (`download?inline=1`) are served with their real type only for
raster images, audio and video; everything else, HTML and SVG included, is
served as `text/plain`, and every download carries
`X-Content-Type-Options: nosniff` and a sandboxing Content-Security-Policy,
so files on the server can't run script in the app's origin.

The editor handles UTF-8 text up to 5 MiB. Saving writes a temporary file
with the original's owner, mode and extended attributes (including ACLs) and
renames it over the original; if the file changed since it was opened the
save is refused (409). As with most editors this gives the file a new inode,
so other hard links keep the old contents.

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

## Container

Images for linux/amd64 and linux/arm64 are published to
`ghcr.io/alanaktion/xwebfm` by the Container workflow: `:edge` from `main`,
and `:X.Y.Z` / `:X.Y` / `:latest` from `vX.Y.Z` tags (which also create a
GitHub release).

The image runs as root and serves the host filesystem mounted at `/host`,
resolving user and group names from the host's `/etc/passwd` and
`/etc/group` (the defaults of its command line):

```sh
docker run -d --name xwebfm --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /:/host \
  ghcr.io/alanaktion/xwebfm:0.1.0
```

- Keep it bound to localhost behind your authenticating proxy, as above.
- Mount a narrower tree (e.g. `-v /srv:/host`) to expose less; with btrfs,
  mount one parent so reflinks work across everything you manage (reflink
  copies can't cross bind mounts).
- Don't use rootless Docker or `userns-remap`: ownership changes need real
  host UIDs.
- Docker's default seccomp profile allows `openat2` (Docker 20.10+).
- Override the arguments to change flags, e.g.
  `… ghcr.io/alanaktion/xwebfm:0.1.0 -listen 0.0.0.0:8080 -root /host -passwd /host/etc/passwd -group /host/etc/group -jobs 4`.
- `xwebfm -version` prints the build version.

## API

All endpoints return JSON; errors look like
`{"error": {"code": "not_found", "message": "..."}}`. POST requests must use
`Content-Type: application/json` (uploads instead send an `X-Requested-With`
header). `dst` is always the full destination path. "owner…" means
`"owner": "keep|inherit|set"` plus `uid`/`user` and `gid`/`group` for `set`.

| Method | Path | Body / query |
| --- | --- | --- |
| GET | `/api/v1/fs/list` | `?path=/dir` → `{path, dir, fs, entries}` (`fs`: type, free/total, reflink support) |
| GET | `/api/v1/fs/stat` | `?path=/file` |
| GET | `/api/v1/fs/download` | `?path=` (files support ranges; directories stream as `.tar.gz`) |
| POST | `/api/v1/fs/chown` | `{"path", "uid" or "user", "gid" or "group", "recursive"}` |
| POST | `/api/v1/fs/chmod` | `{"path", "mode": "0755"` or `"dirMode": "755", "fileMode": "644", "recursive"}` |
| POST | `/api/v1/fs/copy` | `{"src", "dst", "reflink": "auto\|always\|never", "overwrite", owner…}` → counts of files, dirs, reflinked, bytes |
| POST | `/api/v1/fs/move` | `{"src", "dst", "overwrite", owner…}` |
| POST | `/api/v1/fs/rename` | `{"path", "name"}` |
| POST | `/api/v1/fs/mkdir` | `{"path", "mode", owner…}` (owner defaults to `inherit`) |
| POST | `/api/v1/fs/delete` | `{"path"}` (recursive) |
| POST | `/api/v1/fs/upload` | multipart; `?dir=&overwrite=&owner=` (owner defaults to `inherit`) |
| GET | `/api/v1/fs/props` | `?path=` → `{entry, acl, flags}` |
| POST | `/api/v1/fs/acl` | `{"path", "acl": {"named": [{"kind": "user\|group", "id", "perm": 0-7}], "default": [...], "hasDefault"}}` |
| POST | `/api/v1/fs/flags` | `{"path", "flags": {"immutable", "appendOnly", "noDump", "noAtime", "noCow"}}` |
| POST | `/api/v1/fs/link` | `{"target", "path", "kind": "symlink\|hard", "relative"}` |
| POST | `/api/v1/fs/write` | `{"path", "content", "expectSize", "expectMtime"}` (409 if changed) |
| GET | `/api/v1/users`, `/api/v1/groups` | users include `home` |
| POST | `/api/v1/jobs` | `{"kind": "copy\|move", "items": [{"src", "dst"}], …}` or `{"kind": "delete\|chown\|chmod", "paths": [...], …}` with the same options as the synchronous endpoints; `{"kind": "extract", "items": [{"src": archive, "dst": dir}], "password", "deleteOriginals", owner…}`; `{"kind": "archive", "paths": [...], "dst": file, "format", "overwrite", "deleteOriginals", owner…}`; `{"kind": "usage", "paths": [dir]}`; `{"kind": "find", "paths": [dir], "query": {"name", "type", "uid", "gid", "minSize", "maxSize", "modifiedAfter", "modifiedBefore", "orphaned", "foreignOwner", "worldWritable", "setuid", "crossMounts"}}`; `{"kind": "fixowner", "paths": [...], "match": "self\|parent"}`; `{"kind": "acl", "paths": [...], "acl": {…}, "recursive"}` → 202 + job |
| GET | `/api/v1/jobs` | all jobs |
| GET | `/api/v1/jobs/{id}/result` | result of a `usage` or `find` job |
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

