import { SvelteSet } from 'svelte/reactivity'
import { api, type Entry } from './api'
import { joinPath, parentPath } from './format'

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
  #anchor = 0 // start of a shift-click range

  rows: Row[] = $derived([
    ...(this.path === '/' ? [] : [{ parent: true as const, path: parentPath(this.path) }]),
    ...this.entries.map((entry) => ({ parent: false as const, entry })),
  ])

  constructor(path = '/') {
    this.path = path
  }

  async load() {
    this.loading = true
    try {
      const res = await api.list(this.path)
      this.path = res.path
      this.entries = res.entries
      this.error = ''
      const present = new Set(res.entries.map((e) => e.path))
      for (const p of this.selected) if (!present.has(p)) this.selected.delete(p)
    } catch (e) {
      this.entries = []
      this.error = (e as Error).message
    } finally {
      this.cursor = Math.min(this.cursor, Math.max(this.rows.length - 1, 0))
      this.loading = false
    }
  }

  navigate(path: string) {
    // When going up, put the cursor on the directory we came from.
    const from = this.path
    this.path = path
    this.selected.clear()
    this.cursor = 0
    this.load().then(() => {
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
