import type { Entry } from './api'

const typeChar: Record<string, string> = {
  dir: 'd', symlink: 'l', fifo: 'p', socket: 's', block: 'b', char: 'c', file: '-', unknown: '?',
}

/** Formats a mode like `ls -l`, e.g. "drwxr-sr-x". */
export function modeString(type: Entry['type'], mode: number): string {
  const rwx = (bits: number, special: boolean, ch: string) =>
    (bits & 4 ? 'r' : '-') +
    (bits & 2 ? 'w' : '-') +
    (special ? (bits & 1 ? ch : ch.toUpperCase()) : bits & 1 ? 'x' : '-')
  return (
    typeChar[type] +
    rwx((mode >> 6) & 7, (mode & 0o4000) !== 0, 's') +
    rwx((mode >> 3) & 7, (mode & 0o2000) !== 0, 's') +
    rwx(mode & 7, (mode & 0o1000) !== 0, 't')
  )
}

export function octal(mode: number): string {
  return mode.toString(8).padStart(4, '0')
}

export function humanSize(n: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return i === 0 ? `${n} B` : `${n.toFixed(1)} ${units[i]}`
}

export function joinPath(dir: string, name: string): string {
  return dir === '/' ? `/${name}` : `${dir}/${name}`
}

export function parentPath(p: string): string {
  const i = p.lastIndexOf('/')
  return i <= 0 ? '/' : p.slice(0, i)
}

export type ArchiveFormat =
  | 'zip' | '7z' | 'rar' | 'tar' | 'tar.gz' | 'tar.bz2' | 'tar.xz' | 'tar.zst'
  | 'gz' | 'bz2' | 'xz' | 'zst'

export interface ArchiveInfo {
  format: ArchiveFormat
  /** Name without archive/volume suffixes (the extraction folder name). */
  base: string
}

// Mirrors fsops.DetectArchive on the server; longest suffixes first.
const archiveSuffixes: [string, ArchiveFormat][] = [
  ['.tar.gz', 'tar.gz'], ['.tgz', 'tar.gz'],
  ['.tar.bz2', 'tar.bz2'], ['.tbz2', 'tar.bz2'], ['.tbz', 'tar.bz2'],
  ['.tar.xz', 'tar.xz'], ['.txz', 'tar.xz'],
  ['.tar.zst', 'tar.zst'], ['.tzst', 'tar.zst'],
  ['.tar', 'tar'], ['.zip', 'zip'], ['.7z', '7z'], ['.rar', 'rar'],
  ['.gz', 'gz'], ['.bz2', 'bz2'], ['.xz', 'xz'], ['.zst', 'zst'],
]

/** Recognises archives (including multi-volume parts) by file name. */
export function archiveInfo(name: string): ArchiveInfo | null {
  let m = /^(.+)\.part\d+\.rar$/i.exec(name) ?? /^(.+)\.r\d\d$/i.exec(name)
  if (m) return { format: 'rar', base: m[1] }
  m = /^(.+)\.(7z|zip)\.\d{3}$/i.exec(name)
  if (m) return { format: m[2].toLowerCase() as ArchiveFormat, base: m[1] }
  const lower = name.toLowerCase()
  for (const [ext, format] of archiveSuffixes) {
    if (lower.endsWith(ext) && name.length > ext.length) return { format, base: name.slice(0, -ext.length) }
  }
  return null
}
