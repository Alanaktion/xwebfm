// Per-browser preferences, kept in localStorage (which may be unavailable).

function read<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(`xwebfm.${key}`)
    return v === null ? fallback : (JSON.parse(v) as T)
  } catch {
    return fallback
  }
}

export function persist(key: string, value: unknown) {
  try {
    localStorage.setItem(`xwebfm.${key}`, JSON.stringify(value))
  } catch {
    // Private mode or storage disabled: settings just don't stick.
  }
}

import { defaultColumns } from './columns'

export const settings = $state({
  // Merged over the defaults so columns added later get their default.
  columns: { ...defaultColumns(), ...read('columns', {}) },
  showHidden: read('showHidden', true),
  sidebar: read('sidebar', true),
  bookmarks: read<string[]>('bookmarks', []),
})

export function loadSetting<T>(key: string, fallback: T): T {
  return read(key, fallback)
}
