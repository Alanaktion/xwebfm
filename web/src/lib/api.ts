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
  /** Bytes allocated on disk. */
  disk: number
  /** Has an extended POSIX ACL. */
  acl?: boolean
}

export interface FSInfo {
  type: string
  total: number
  free: number
  avail: number
  reflink: boolean
}

export interface Account {
  id: number
  name: string
  home?: string
}

export interface ListResponse {
  path: string
  dir?: Entry
  fs?: FSInfo
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

export type JobState = 'queued' | 'running' | 'done' | 'failed' | 'cancelled'

export interface JobProgress {
  totalEntries: number
  totalBytes: number
  entries: number
  bytes: number
  current?: string
}

export interface Job {
  id: string
  kind: 'copy' | 'move' | 'delete' | 'chown' | 'chmod' | 'extract' | 'archive' | 'usage' | 'find' | 'fixowner' | 'acl'
  title: string
  state: JobState
  items: { src: string; dst?: string }[]
  created: string
  started?: string
  finished?: string
  progress: JobProgress
  errors?: string[]
  dirs?: string[]
  hasResult?: boolean
}

export interface UsageItem {
  name: string
  path: string
  type: FileType
  size: number
  disk: number
  entries: number
}

export interface UsageReport {
  path: string
  size: number
  disk: number
  entries: number
  children: UsageItem[]
  byOwner: { uid: number; user?: string; size: number; disk: number; entries: number }[]
}

export interface ACLEntry {
  kind: 'user' | 'group'
  id: number
  name?: string
  /** rwx bits: 4 read, 2 write, 1 execute */
  perm: number
}

export interface ACL {
  named: ACLEntry[]
  mask?: number
  default: ACLEntry[]
  hasDefault: boolean
}

export interface Flags {
  immutable: boolean
  appendOnly: boolean
  noDump: boolean
  noAtime: boolean
  noCow: boolean
}

export interface Props {
  entry: Entry
  acl: ACL | null
  flags: Flags | null
}

export interface FindQuery {
  name?: string
  type?: FileType
  uid?: number
  gid?: number
  minSize?: number
  maxSize?: number
  modifiedAfter?: string
  modifiedBefore?: string
  orphaned?: boolean
  foreignOwner?: boolean
  worldWritable?: boolean
  setuid?: boolean
  crossMounts?: boolean
}

export interface FindResult {
  root: string
  entries: Entry[]
  truncated: boolean
  scanned: number
}

export type JobRequest =
  | ({ kind: 'copy'; items: { src: string; dst: string }[]; reflink: ReflinkMode; overwrite: boolean } & OwnerFields)
  | ({ kind: 'move'; items: { src: string; dst: string }[]; overwrite: boolean } & OwnerFields)
  | { kind: 'delete'; paths: string[] }
  | { kind: 'chown'; paths: string[]; uid?: number; gid?: number; recursive: boolean }
  | { kind: 'chmod'; paths: string[]; mode?: string; dirMode?: string; fileMode?: string; recursive: boolean }
  | { kind: 'fixowner'; paths: string[]; match: 'self' | 'parent' }
  | { kind: 'acl'; paths: string[]; acl: ACL; recursive: boolean }
  | { kind: 'usage'; paths: [string] }
  | { kind: 'find'; paths: [string]; query: FindQuery }
  | ({
      kind: 'extract'
      items: { src: string; dst: string }[]
      password?: string
      deleteOriginals: boolean
    } & OwnerFields)
  | ({
      kind: 'archive'
      paths: string[]
      dst: string
      format: string
      overwrite: boolean
      deleteOriginals: boolean
    } & OwnerFields)

export interface ServerInfo {
  archive: { create: string[] }
}

/** Events streamed over /api/v1/ws. */
export type JobEvent =
  | { type: 'snapshot'; jobs: Job[] }
  | { type: 'job'; job: Job }
  | { type: 'removed'; id: string }
  | { type: 'changed'; dirs?: string[] }

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
  chmod: (req: { path: string; mode?: string; dirMode?: string; fileMode?: string; recursive?: boolean }) =>
    request<Entry>('POST', '/api/v1/fs/chmod', req),
  copy: (req: { src: string; dst: string; reflink: ReflinkMode; overwrite: boolean } & OwnerFields) =>
    request<CopyResult & { entry: Entry }>('POST', '/api/v1/fs/copy', req),
  move: (req: { src: string; dst: string; overwrite: boolean } & OwnerFields) =>
    request<MoveResult & { entry: Entry }>('POST', '/api/v1/fs/move', req),
  rename: (path: string, name: string) => request<Entry>('POST', '/api/v1/fs/rename', { path, name }),
  remove: (path: string) => request<null>('POST', '/api/v1/fs/delete', { path }),
  mkdir: (req: { path: string; mode?: string } & OwnerFields) => request<Entry>('POST', '/api/v1/fs/mkdir', req),
  info: () => request<ServerInfo>('GET', '/api/v1/info'),
  props: (path: string) => request<Props>('GET', `/api/v1/fs/props${q(path)}`),
  setAcl: (path: string, acl: ACL) => request<Props>('POST', '/api/v1/fs/acl', { path, acl }),
  setFlags: (path: string, flags: Flags) => request<Props>('POST', '/api/v1/fs/flags', { path, flags }),
  write: (req: { path: string; content: string; expectSize: number; expectMtime: string }) =>
    request<Entry>('POST', '/api/v1/fs/write', req),
  link: (req: { target: string; path: string; kind: 'symlink' | 'hard'; relative: boolean }) =>
    request<Entry>('POST', '/api/v1/fs/link', req),
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

  jobs: {
    submit: (req: JobRequest) => request<Job>('POST', '/api/v1/jobs', req),
    cancel: (id: string) => request<Job>('POST', `/api/v1/jobs/${id}/cancel`, {}),
    dismiss: (id: string) => request<null>('POST', `/api/v1/jobs/${id}/dismiss`, {}),
    clear: () => request<null>('POST', '/api/v1/jobs/clear', {}),
    result: <T>(id: string) => request<T>('GET', `/api/v1/jobs/${id}/result`),
  },
}
