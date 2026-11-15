<script lang="ts">
  import { onMount } from 'svelte'
  import Modal from '../Modal.svelte'
  import { humanSize } from '../format'
  import DataGrid, { type GridColumn } from './DataGrid.svelte'
  import type { ToolProps } from '.'
  import { sqlite, type SqlObject, type SqlPage, type SqlResult, type SqlValue } from './sqlite'

  let { entry, onclose, ondone }: ToolProps = $props()

  const PAGE = 100
  // svelte-ignore state_referenced_locally
  const db = sqlite(entry.path)

  let objects = $state<SqlObject[]>([])
  let current = $state<string | null>(null)
  let tab = $state<'data' | 'sql'>('data')
  let page = $state<SqlPage | null>(null)
  let offset = $state(0)
  let sort = $state<{ column: string; desc: boolean } | null>(null)
  let selected = $state<number[]>([])
  let adding = $state(false)
  let error = $state('')
  let info = $state('')
  let loading = $state(false)

  let sql = $state('')
  let result = $state<SqlResult | null>(null)
  let running = $state(false)

  let currentObject = $derived(objects.find((o) => o.name === current))
  let canEdit = $derived(!!page && page.key.length > 0)
  let columns = $derived<GridColumn[]>(
    page?.columns.map((c) => ({
      name: c.name,
      detail: [c.type, c.pk ? 'PK' : '', c.notNull ? 'NOT NULL' : ''].filter(Boolean).join(' ') || undefined,
      title: c.default !== undefined ? `Default: ${c.default}` : undefined,
      readonly: c.generated,
    })) ?? [],
  )

  onMount(loadObjects)

  async function loadObjects() {
    error = ''
    try {
      objects = await db.objects()
      if (!current || !objects.some((o) => o.name === current)) {
        const user = objects.filter((o) => !o.name.startsWith('sqlite_'))
        const first = user.find((o) => o.type === 'table') ?? user[0] ?? objects[0]
        if (first) open(first.name)
        else {
          current = null
          page = null
          tab = 'sql'
        }
      }
    } catch (e) {
      error = (e as Error).message
    }
  }

  function open(name: string) {
    current = name
    offset = 0
    sort = null
    adding = false
    tab = 'data'
    if (!sql) sql = `SELECT * FROM "${name.replaceAll('"', '""')}" LIMIT 100;`
    loadPage()
  }

  async function loadPage() {
    if (!current) return
    loading = true
    error = ''
    try {
      page = await db.rows({ table: current, offset, limit: PAGE, order: sort?.column, desc: sort?.desc })
      selected = []
    } catch (e) {
      error = (e as Error).message
    } finally {
      loading = false
    }
  }

  function onsort(column: string) {
    if (sort?.column !== column) sort = { column, desc: false }
    else if (!sort.desc) sort = { column, desc: true }
    else sort = null
    offset = 0
    loadPage()
  }

  function go(to: number) {
    offset = Math.max(0, to)
    loadPage()
  }

  // Edits throw on failure so the grid keeps the editor open.
  async function fail(e: unknown): Promise<never> {
    error = (e as Error).message
    throw e
  }

  async function onedit(row: number, col: number, value: SqlValue) {
    if (!page) return
    error = ''
    await db.update(page.table, page.rows[row].key!, page.columns[col].name, value).catch(fail)
    info = `Updated ${page.columns[col].name}`
    await loadPage()
  }

  async function oninsert(values: Record<string, SqlValue>) {
    if (!page) return
    error = ''
    const res = await db.insert(page.table, values).catch(fail)
    adding = false
    info = res.rowid ? `Inserted row ${res.rowid}` : 'Inserted a row'
    await loadPage()
  }

  async function deleteSelected() {
    if (!page || !selected.length) return
    const n = selected.length
    if (!confirm(`Delete ${n} row${n === 1 ? '' : 's'} from ${page.table}? This can't be undone.`)) return
    error = ''
    try {
      const res = await db.remove(page.table, selected.map((i) => page!.rows[i].key!))
      info = `Deleted ${res.deleted} row${res.deleted === 1 ? '' : 's'}`
      ondone(`${entry.name}: ${info.toLowerCase()}`)
      await loadPage()
    } catch (e) {
      error = (e as Error).message
    }
  }

  async function run() {
    if (running || !sql.trim()) return
    running = true
    error = ''
    try {
      result = await db.query(sql)
      // The query may have created, dropped or changed tables.
      const before = JSON.stringify(objects)
      objects = await db.objects()
      if (JSON.stringify(objects) !== before || result.changes) {
        if (current && !objects.some((o) => o.name === current)) current = null
        page = null
      }
    } catch (e) {
      error = (e as Error).message
      result = null
    } finally {
      running = false
    }
  }

  function sqlKey(ev: KeyboardEvent) {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === 'Enter') {
      ev.preventDefault()
      run()
    }
  }

  function showData() {
    tab = 'data'
    if (!page && current) loadPage()
  }

  let resultColumns = $derived<GridColumn[]>(result?.columns.map((name) => ({ name })) ?? [])
  let resultSummary = $derived.by(() => {
    if (!result) return ''
    const parts: string[] = []
    if (result.columns.length) parts.push(`${result.rows.length}${result.truncated ? '+' : ''} row${result.rows.length === 1 ? '' : 's'}`)
    if (result.changes || !result.columns.length) parts.push(`${result.changes} row${result.changes === 1 ? '' : 's'} changed`)
    if (result.statements > 1) parts.push(`${result.statements} statements`)
    return parts.join(' · ')
  })
</script>

<Modal title="SQLite: {entry.name}" full {error} {onclose}>
  <div class="sqlite">
    <aside>
      <div class="meta muted"><span class="mono" title={entry.path}>{entry.path}</span> · {humanSize(entry.size)}</div>
      <ul>
        {#each objects as o}
          <li>
            <button
              type="button"
              class:on={tab === 'data' && current === o.name}
              class:system={o.name.startsWith('sqlite_') || o.type === 'shadow'}
              title={o.sql || o.name}
              onclick={() => open(o.name)}
            >
              <span class="oname">{o.name}</span>
              {#if o.type !== 'table'}<span class="otype">{o.type}</span>{/if}
            </button>
          </li>
        {:else}
          <li class="muted empty">No tables</li>
        {/each}
      </ul>
    </aside>

    <section>
      <div class="bar">
        <div class="tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === 'data'} class:on={tab === 'data'} onclick={showData} disabled={!current}>
            {current ?? 'Data'}
          </button>
          <button type="button" role="tab" aria-selected={tab === 'sql'} class:on={tab === 'sql'} onclick={() => (tab = 'sql')}>SQL</button>
        </div>
        {#if tab === 'data' && page}
          {#if canEdit}
            <button type="button" onclick={() => (adding = !adding)} class:on={adding}>Add row</button>
            <button type="button" onclick={deleteSelected} disabled={!selected.length}>
              Delete{selected.length ? ` ${selected.length}` : ''}
            </button>
          {:else}
            <span class="muted small" title="Views, virtual tables and tables whose rowid is hidden by other columns can't be edited here; use SQL instead">read-only</span>
          {/if}
          <button type="button" onclick={loadPage} disabled={loading} title="Reload">↻</button>
          <span class="spacer"></span>
          {#if info}<span class="info small" title={info}>{info}</span>{/if}
          <span class="pager">
            <button type="button" onclick={() => go(offset - PAGE)} disabled={offset === 0 || loading} aria-label="Previous page">‹</button>
            <span class="small">
              {page.total ? `${(offset + 1).toLocaleString()}–${(offset + page.rows.length).toLocaleString()}` : '0'} of {page.total.toLocaleString()}
            </span>
            <button type="button" onclick={() => go(offset + PAGE)} disabled={offset + PAGE >= page.total || loading} aria-label="Next page">›</button>
          </span>
        {:else if tab === 'sql'}
          <button type="button" class="primary" onclick={run} disabled={running || !sql.trim()} title="Ctrl+Enter">
            {running ? 'Running…' : 'Run'}
          </button>
          <span class="spacer"></span>
          {#if resultSummary}<span class="info small">{resultSummary}</span>{/if}
        {/if}
      </div>

      {#if tab === 'data'}
        {#if page}
          {#if currentObject?.withoutRowid}<p class="muted small note">WITHOUT ROWID table: rows are identified by their primary key.</p>{/if}
          <DataGrid
            {columns}
            rows={page.rows.map((r) => r.values)}
            {offset}
            {sort}
            {onsort}
            onedit={canEdit ? onedit : undefined}
            bind:selected
            {adding}
            {oninsert}
            oncanceladd={() => (adding = false)}
          />
          {#if canEdit}<p class="muted small note">Double-click a cell to edit it: Enter saves, Esc cancels.</p>{/if}
        {:else if loading}
          <p class="muted">Loading…</p>
        {:else}
          <p class="muted">Pick a table.</p>
        {/if}
      {:else}
        <!-- svelte-ignore a11y_autofocus -->
        <textarea class="mono" bind:value={sql} onkeydown={sqlKey} spellcheck="false" autofocus placeholder="SELECT * FROM …"></textarea>
        {#if result?.note}<p class="warn small note">{result.note}</p>{/if}
        {#if result?.columns.length}
          <DataGrid columns={resultColumns} rows={result.rows} />
          {#if result.truncated}<p class="muted small note">Showing the first {result.rows.length.toLocaleString()} rows.</p>{/if}
        {:else}
          <p class="muted small note">
            Ctrl+Enter runs the SQL. Several statements separated by semicolons run in order, in autocommit mode
            unless you BEGIN a transaction; the rows of the last query are shown. ATTACH can only reach files in
            the same folder.
          </p>
        {/if}
      {/if}
    </section>
  </div>
</Modal>

<style>
  .sqlite { flex: 1; min-height: 0; display: grid; grid-template-columns: 200px 1fr; gap: 12px; }
  @media (max-width: 700px) {
    .sqlite { grid-template-columns: 1fr; grid-template-rows: auto 1fr; }
    aside { max-height: 25vh; }
  }
  aside { display: flex; flex-direction: column; min-height: 0; border: 1px solid var(--border); border-radius: 6px; overflow: hidden; }
  .meta { padding: 6px 8px; font-size: 11px; border-bottom: 1px solid var(--border); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta .mono { font-size: 11px; }
  ul { list-style: none; margin: 0; padding: 4px; overflow: auto; flex: 1; }
  li button {
    display: flex;
    width: 100%;
    gap: 6px;
    align-items: baseline;
    border: none;
    background: none;
    text-align: left;
    padding: 3px 6px;
    border-radius: 4px;
  }
  li button:hover { background: var(--hover); }
  li button.on { background: var(--selected); }
  li button.system { color: var(--muted); }
  .oname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .otype { font-size: 10px; color: var(--muted); text-transform: uppercase; }
  .empty { padding: 8px; }
  section { display: flex; flex-direction: column; min-width: 0; min-height: 0; gap: 6px; }
  .bar { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .bar button { padding: 2px 10px; }
  .tabs { display: flex; gap: 0; margin-right: 6px; }
  .tabs button { border-radius: 0; max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tabs button:first-child { border-radius: 6px 0 0 6px; }
  .tabs button:last-child { border-radius: 0 6px 6px 0; border-left: none; }
  button.on { border-color: var(--accent); color: var(--accent); }
  .spacer { flex: 1; }
  .pager { display: inline-flex; gap: 4px; align-items: center; white-space: nowrap; }
  .pager button { padding: 0 8px; }
  .small { font-size: 12px; }
  .info { color: var(--ok); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 40ch; }
  .note { margin: 0; }
  .warn { color: var(--warn); }
  .muted { color: var(--muted); }
  textarea {
    width: 100%;
    min-height: 110px;
    height: 22vh;
    resize: vertical;
    font-size: 12px;
    line-height: 1.45;
    padding: 8px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--fg);
    tab-size: 4;
  }
</style>
