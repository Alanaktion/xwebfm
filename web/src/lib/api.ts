// Typed client for the xwebfm JSON API. Types mirror internal/fsops.

export type FileType = 'file' | 'dir' | 'symlink' | 'fifo' | 'socket' | 'block' | 'char' | 'unknown'

export interface Entry {
  name: string
  path: string
  type: FileType
  size: number
  mode: number
  uid: number
  gid: number
  user?: string
  group?: string
  nlink: number
  inode: number
  mtime: string
  linkTarget?: string
}

export interface Account {
  id: number
  name: string
}

export interface ListResponse {
  path: string
  entries: Entry[]
}

export type ReflinkMode = 'auto' | 'always' | 'never'

export type OwnerPolicy = 'keep' | 'inherit' | 'set'

/** Owner policy for operations that create files; IDs/names apply to "set". */
export interface OwnerFields {
  owner?: OwnerPolicy
  uid?: number
  user?: string
  gid?: number
  group?: string
}

export interface CopyResult {
  files: number
  dirs: number
  reflinked: number
  skipped: number
  bytes: number
}

export interface MoveResult {
  copied: boolean
  copy?: CopyResult
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message)
  }
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new ApiError(res.status, data?.error?.code ?? 'unknown', data?.error?.message ?? res.statusText)
  }
  return data as T
}

const q = (path: string) => `?path=${encodeURIComponent(path)}`

export const api = {
  list: (path: string) => request<ListResponse>('GET', `/api/v1/fs/list${q(path)}`),
  stat: (path: string) => request<Entry>('GET', `/api/v1/fs/stat${q(path)}`),
  chown: (req: { path: string; uid?: number; gid?: number; recursive?: boolean }) =>
    request<Entry>('POST', '/api/v1/fs/chown', req),
  chmod: (req: { path: string; mode: string; recursive?: boolean }) =>
    request<Entry>('POST', '/api/v1/fs/chmod', req),
  copy: (req: { src: string; dst: string; reflink: ReflinkMode; overwrite: boolean } & OwnerFields) =>
    request<CopyResult & { entry: Entry }>('POST', '/api/v1/fs/copy', req),
  move: (req: { src: string; dst: string; overwrite: boolean } & OwnerFields) =>
    request<MoveResult & { entry: Entry }>('POST', '/api/v1/fs/move', req),
  rename: (path: string, name: string) => request<Entry>('POST', '/api/v1/fs/rename', { path, name }),
  remove: (path: string) => request<null>('POST', '/api/v1/fs/delete', { path }),
  mkdir: (req: { path: string; mode?: string } & OwnerFields) => request<Entry>('POST', '/api/v1/fs/mkdir', req),
  users: () => request<Account[]>('GET', '/api/v1/users'),
  groups: () => request<Account[]>('GET', '/api/v1/groups'),

  /** Uploads one file into dir; new files inherit the directory's owner. */
  async upload(dir: string, file: File, overwrite = false): Promise<Entry> {
    const body = new FormData()
    body.append('file', file, file.name)
    const params = new URLSearchParams({ dir, overwrite: String(overwrite) })
    const res = await fetch(`/api/v1/fs/upload?${params}`, {
      method: 'POST',
      headers: { 'X-Requested-With': 'xwebfm' },
      body,
    })
    const data = await res.json().catch(() => null)
    if (!res.ok) {
      throw new ApiError(res.status, data?.error?.code ?? 'unknown', data?.error?.message ?? res.statusText)
    }
    return (data as Entry[])[0]
  },

  downloadUrl: (path: string) => `/api/v1/fs/download${q(path)}`,
}
