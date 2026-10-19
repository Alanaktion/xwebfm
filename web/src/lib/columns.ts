// Optional columns of the file listings (Name is always shown).

export type ColumnKey = 'size' | 'disk' | 'mode' | 'owner' | 'mtime'

export const COLUMNS: { key: ColumnKey; label: string; help: string; default: boolean }[] = [
  { key: 'size', label: 'Size', help: 'Apparent size (folder totals after a disk-usage scan)', default: true },
  { key: 'disk', label: 'On disk', help: 'Space allocated on disk (sparse, compressed or reflinked files differ)', default: false },
  { key: 'mode', label: 'Mode', help: 'Permissions, with + when an ACL is set', default: true },
  { key: 'owner', label: 'Owner', help: 'user:group', default: true },
  { key: 'mtime', label: 'Modified', help: 'Last modification time', default: true },
]

export const defaultColumns = (): Record<ColumnKey, boolean> =>
  Object.fromEntries(COLUMNS.map((c) => [c.key, c.default])) as Record<ColumnKey, boolean>
