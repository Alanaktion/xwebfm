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
