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

const HIDE_STATUS_COOKIE = 'xwebfm_hide_status'

function readHideStatus(): boolean {
  try {
    return document.cookie.split('; ').some((c) => c === `${HIDE_STATUS_COOKIE}=1`)
  } catch {
    return false
  }
}

/** Remembers the hide-status preference in a cookie (one year, this site only). */
export function persistHideStatus(hide: boolean) {
  try {
    document.cookie = `${HIDE_STATUS_COOKIE}=${hide ? 1 : 0}; path=/; max-age=${hide ? 31536000 : 0}; SameSite=Lax`
  } catch {
    // Cookies disabled: the preference just doesn't stick.
  }
}

export const settings = $state({
  // Merged over the defaults so columns added later get their default.
  columns: { ...defaultColumns(), ...read('columns', {}) },
  showHidden: read('showHidden', true),
  sidebar: read('sidebar', true),
  bookmarks: read<string[]>('bookmarks', []),
  // When set, the status/result bar stays collapsed until revealed by hand.
  hideStatus: readHideStatus(),
})

export function loadSetting<T>(key: string, fallback: T): T {
  return read(key, fallback)
}
