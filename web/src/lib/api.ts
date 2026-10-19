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

export interface CopyResponse {
  reflinked: boolean
  bytes: number
  entry: Entry
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
  copy: (req: { src: string; dst: string; reflink: ReflinkMode; preserve: boolean; overwrite: boolean }) =>
    request<CopyResponse>('POST', '/api/v1/fs/copy', req),
  users: () => request<Account[]>('GET', '/api/v1/users'),
  groups: () => request<Account[]>('GET', '/api/v1/groups'),
}
