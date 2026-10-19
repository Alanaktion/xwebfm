import { SvelteMap, SvelteSet } from 'svelte/reactivity'
import { api, type Entry, type FindResult, type FSInfo } from './api'
import { joinPath, parentPath } from './format'

/** Search results shown in place of a directory listing. */
export interface PaneResults {
  label: string
  root: string
  truncated: boolean
}

/** A row in a pane: a real entry, or the ".." link to the parent. */
export type Row = { parent: true; path: string } | { parent: false; entry: Entry }

/** State of one file pane: its directory, listing, cursor and selection. */
export class PaneState {
  path = $state('/')
  entries = $state<Entry[]>([])
  selected = new SvelteSet<string>()
  cursor = $state(0)
  loading = $state(false)
  error = $state('')
  /** The path whose listing is in entries; differs from path while loading a new one. */
  loadedPath = $state<string | null>(null)
  /** The listed directory itself and its filesystem. */
  dir = $state<Entry | null>(null)
  fs = $state<FSInfo | null>(null)
  /** Set while showing search results instead of the directory. */
  results = $state<PaneResults | null>(null)
  resultEntries = $state<Entry[]>([])
  /** Folder sizes computed by disk-usage scans, by path. */
  dirSizes = new SvelteMap<string, number>()
  #anchor = 0 // start of a shift-click range
  #seq = 0 // identifies the latest load, so slower stale responses are dropped

  /** Set while a new directory is loading and nothing from it can be shown yet. */
  stale = $derived(this.loadedPath !== this.path)

  rows: Row[] = $derived(
    this.results
      ? this.resultEntries.map((entry) => ({ parent: false as const, entry }))
      : this.stale
      ? []
      : [
          ...(this.path === '/' ? [] : [{ parent: true as const, path: parentPath(this.path) }]),
          ...this.entries.map((entry) => ({ parent: false as const, entry })),
        ],
  )

  constructor(path = '/') {
    this.path = path
  }

  /** Loads (or refreshes) the listing of path. Resolves false if superseded. */
  async load(): Promise<boolean> {
    if (this.results) return this.#refreshResults()
    const seq = ++this.#seq
    const path = this.path
    this.loading = true
    try {
      const res = await api.list(path)
      if (seq !== this.#seq) return false
      this.path = res.path
      this.entries = res.entries
      this.dir = res.dir ?? null
      this.fs = res.fs ?? null
      this.error = ''
      const present = new Set(res.entries.map((e) => e.path))
      for (const p of this.selected) if (!present.has(p)) this.selected.delete(p)
    } catch (e) {
      if (seq !== this.#seq) return false
      this.entries = []
      this.error = (e as Error).message
    }
    this.loadedPath = this.path
    this.cursor = Math.min(this.cursor, Math.max(this.rows.length - 1, 0))
    this.loading = false
    return true
  }

  /** Shows search results in place of the listing. */
  showResults(res: FindResult, label: string) {
    this.results = { label, root: res.root, truncated: res.truncated }
    this.resultEntries = res.entries
    this.selected.clear()
    this.cursor = 0
  }

  /** Re-stats result entries after changes, dropping ones that are gone. */
  async #refreshResults(): Promise<boolean> {
    const entries = this.resultEntries
    if (entries.length > 500) return true
    const fresh = await Promise.all(entries.map((e) => api.stat(e.path).catch(() => null)))
    if (this.resultEntries !== entries) return false
    this.resultEntries = fresh.filter((e): e is Entry => e !== null)
    const present = new Set(this.resultEntries.map((e) => e.path))
    for (const p of this.selected) if (!present.has(p)) this.selected.delete(p)
    this.cursor = Math.min(this.cursor, Math.max(this.rows.length - 1, 0))
    return true
  }

  navigate(path: string) {
    this.results = null
    this.resultEntries = []
    // When going up, put the cursor on the directory we came from.
    const from = this.path
    this.path = path
    // Drop the old listing right away rather than showing it until the new
    // one arrives.
    this.entries = []
    this.error = ''
    this.loadedPath = null
    this.selected.clear()
    this.cursor = 0
    this.load().then((current) => {
      if (!current) return
      const i = this.rows.findIndex((r) => !r.parent && r.entry.path === from)
      if (i >= 0) this.cursor = i
    })
  }

  /** The entries an operation applies to: the selection, else the cursor row. */
  targets(): Entry[] {
    if (this.selected.size) return this.entries.filter((e) => this.selected.has(e.path))
    const row = this.rows[this.cursor]
    return row && !row.parent ? [row.entry] : []
  }

  /** Click handling: select the row, Ctrl/Cmd toggles, Shift selects a range. */
  click(i: number, ev: MouseEvent) {
    const row = this.rows[i]
    this.cursor = i
    if (row.parent) {
      this.selected.clear()
      return
    }
    if (ev.shiftKey) {
      this.#selectRange(i, ev.ctrlKey || ev.metaKey)
      return
    }
    this.#anchor = i
    if (ev.ctrlKey || ev.metaKey) {
      this.toggle(row.entry.path)
    } else {
      this.selected.clear()
      this.selected.add(row.entry.path)
    }
  }

  #selectRange(i: number, add: boolean) {
    const [a, b] = [Math.min(this.#anchor, i), Math.max(this.#anchor, i)]
    if (!add) this.selected.clear()
    for (const r of this.rows.slice(a, b + 1)) if (!r.parent) this.selected.add(r.entry.path)
  }

  toggle(path: string) {
    if (this.selected.has(path)) this.selected.delete(path)
    else this.selected.add(path)
  }

  selectAll() {
    for (const e of this.entries) this.selected.add(e.path)
  }

  /**
   * Moves the cursor. With extend (Shift+arrows) the selection grows from
   * the anchor; otherwise a single clicked selection follows the cursor, as
   * in mc where the cursor row is the implicit target. "keep" leaves the
   * selection alone (used after Insert/Space marks a row).
   */
  move(delta: number, mode: 'plain' | 'extend' | 'keep' = 'plain') {
    this.cursor = Math.max(0, Math.min(this.rows.length - 1, this.cursor + delta))
    if (mode === 'extend') {
      this.#selectRange(this.cursor, false)
      return
    }
    this.#anchor = this.cursor
    if (mode === 'plain' && this.selected.size <= 1) this.selected.clear()
  }

  /** Opens a row: enters directories and symlinks, downloads files. */
  open(row: Row | undefined) {
    if (!row) return
    if (row.parent) this.navigate(row.path)
    else if (row.entry.type === 'dir' || row.entry.type === 'symlink') this.navigate(row.entry.path)
    else if (row.entry.type === 'file') location.href = api.downloadUrl(row.entry.path)
  }

  childPath(name: string) {
    return joinPath(this.path, name)
  }
}
