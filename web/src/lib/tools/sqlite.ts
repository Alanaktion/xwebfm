// Client side of the SQLite file tool (internal/sqlitedb).

import { api } from '../api'

/** An SQLite value as the server encodes it (see sqlitedb.Value). */
export type SqlValue =
  | null
  | number
  | string
  | { int: string }
  | { real: string }
  | { blob: string; size: number; truncated?: boolean }
  | { text: string; size: number; truncated: true }

export interface SqlObject {
  name: string
  type: 'table' | 'view' | 'virtual' | 'shadow'
  columns: number
  withoutRowid?: boolean
  sql: string
}

export interface SqlColumn {
  name: string
  type: string
  notNull?: boolean
  default?: string
  pk?: number
  generated?: boolean
}

export interface SqlPage {
  table: string
  type: string
  columns: SqlColumn[]
  /** What identifies a row; empty when the rows can't be edited. */
  key: string[]
  rows: { key?: SqlValue[]; values: SqlValue[] }[]
  total: number
  offset: number
}

export interface SqlResult {
  columns: string[]
  rows: SqlValue[][]
  truncated: boolean
  statements: number
  changes: number
  note?: string
}

export const sqlite = (path: string) => {
  const call = <T>(action: string, args?: unknown) => api.tool<T>('sqlite', action, path, args)
  return {
    objects: () => call<SqlObject[]>('objects'),
    rows: (args: { table: string; offset?: number; limit?: number; order?: string; desc?: boolean }) =>
      call<SqlPage>('rows', args),
    query: (sql: string) => call<SqlResult>('query', { sql }),
    update: (table: string, key: SqlValue[], column: string, value: SqlValue) =>
      call<object>('update', { table, key, column, value }),
    insert: (table: string, values: Record<string, SqlValue>) => call<{ rowid: number }>('insert', { table, values }),
    remove: (table: string, keys: SqlValue[][]) => call<{ deleted: number }>('delete', { table, keys }),
  }
}

function hex(b64: string, max = 16): string {
  const bin = atob(b64)
  let out = ''
  for (let i = 0; i < Math.min(bin.length, max); i++) out += bin.charCodeAt(i).toString(16).padStart(2, '0')
  return out
}

/** How a value is shown in a grid cell. */
export function cellText(v: SqlValue): { text: string; kind: 'null' | 'num' | 'text' | 'blob' } {
  if (v === null) return { text: 'NULL', kind: 'null' }
  if (typeof v === 'number') return { text: String(v), kind: 'num' }
  if (typeof v === 'string') return { text: v, kind: 'text' }
  if ('int' in v) return { text: v.int, kind: 'num' }
  if ('real' in v) return { text: v.real, kind: 'num' }
  if ('blob' in v) {
    const more = v.size > 16 ? '…' : ''
    return { text: `x'${hex(v.blob)}${more}' (${v.size} B)`, kind: 'blob' }
  }
  return { text: `${v.text}…`, kind: 'text' }
}

/** Whether a value can be edited as text in a cell. */
export function editable(v: SqlValue): boolean {
  return v === null || typeof v !== 'object' || 'int' in v || 'real' in v
}

/** The text put in a cell editor. */
export function editText(v: SqlValue): string {
  return v === null ? '' : cellText(v).text
}

/**
 * Converts edited text back to a value. Numbers stay numbers when the text
 * is still numeric; otherwise text is sent and the column's affinity
 * decides how SQLite stores it.
 */
export function parseEdit(text: string, before: SqlValue): SqlValue {
  const numeric = typeof before === 'number' || (before !== null && typeof before === 'object' && ('int' in before || 'real' in before))
  if (numeric && /^\s*-?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?\s*$/i.test(text)) {
    const n = Number(text)
    if (Number.isSafeInteger(n) || !/^-?\d+$/.test(text.trim())) return n
    return { int: text.trim() }
  }
  return text
}
