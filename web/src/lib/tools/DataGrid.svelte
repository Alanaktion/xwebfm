<script lang="ts" module>
  export interface GridColumn {
    name: string
    /** Shown under the name, e.g. the declared type. */
    detail?: string
    title?: string
    /** Can't be edited or set on insert (generated columns). */
    readonly?: boolean
  }
</script>

<script lang="ts">
  import { tick } from 'svelte'
  import { cellText, editable, editText, parseEdit, type SqlValue } from './sqlite'

  let {
    columns,
    rows,
    offset = 0,
    sort = null,
    onsort,
    onedit,
    selected = $bindable([]),
    adding = false,
    oninsert,
    oncanceladd,
  }: {
    columns: GridColumn[]
    rows: SqlValue[][]
    /** Number of the first row minus one, for the row-number column. */
    offset?: number
    sort?: { column: string; desc: boolean } | null
    /** Header clicked; without it the columns don't sort. */
    onsort?: (column: string) => void
    /** Saves an edited cell; without it the grid is read-only. Throw to keep the editor open. */
    onedit?: (row: number, col: number, value: SqlValue) => Promise<void>
    /** Indexes of rows ticked for deletion (editable grids only). */
    selected?: number[]
    /** Shows a row of inputs for a new row at the top. */
    adding?: boolean
    /** Inserts the new row: only filled-in columns are given. */
    oninsert?: (values: Record<string, SqlValue>) => Promise<void>
    oncanceladd?: () => void
  } = $props()

  let editing = $state<{ row: number; col: number; text: string } | null>(null)
  let saving = $state(false)
  let draft = $state<Record<string, string>>({})
  let draftNull = $state<Record<string, boolean>>({})

  $effect(() => {
    if (adding) {
      draft = {}
      draftNull = {}
    }
  })

  async function startEdit(row: number, col: number) {
    if (!onedit || columns[col].readonly || !editable(rows[row][col])) return
    editing = { row, col, text: editText(rows[row][col]) }
    await tick()
    const el = document.querySelector<HTMLInputElement>('.grid input.cell-editor')
    el?.focus()
    el?.select()
  }

  async function commit(value?: SqlValue) {
    if (!editing || saving || !onedit) return
    const { row, col, text } = editing
    const before = rows[row][col]
    const next = value !== undefined ? value : parseEdit(text, before)
    if (value === undefined && text === editText(before) && before !== null) {
      editing = null
      return
    }
    if (value === undefined && before === null && text === '') {
      editing = null
      return
    }
    saving = true
    try {
      await onedit(row, col, next)
      editing = null
    } catch {
      // The parent shows the error; keep the editor so nothing is lost.
    } finally {
      saving = false
    }
  }

  function editorKey(ev: KeyboardEvent) {
    if (ev.key === 'Enter') {
      ev.preventDefault()
      commit()
    } else if (ev.key === 'Escape') {
      ev.preventDefault()
      ev.stopPropagation()
      editing = null
    }
  }

  async function insert() {
    if (!oninsert || saving) return
    const values: Record<string, SqlValue> = {}
    for (const c of columns) {
      if (draftNull[c.name]) values[c.name] = null
      else if ((draft[c.name] ?? '') !== '') values[c.name] = draft[c.name]
    }
    saving = true
    try {
      await oninsert(values)
    } catch {
      // shown by the parent
    } finally {
      saving = false
    }
  }

  function draftKey(ev: KeyboardEvent) {
    if (ev.key === 'Enter') {
      ev.preventDefault()
      insert()
    } else if (ev.key === 'Escape') {
      ev.preventDefault()
      ev.stopPropagation()
      oncanceladd?.()
    }
  }

  function toggle(i: number) {
    selected = selected.includes(i) ? selected.filter((x) => x !== i) : [...selected, i]
  }
</script>

<div class="grid">
  <table>
    <thead>
      <tr>
        <th class="rownum">
          {#if onedit && rows.length}
            <input
              type="checkbox"
              title="Select all rows on this page"
              checked={selected.length === rows.length}
              onchange={(e) => (selected = e.currentTarget.checked ? rows.map((_, i) => i) : [])}
            />
          {/if}
        </th>
        {#each columns as c}
          <th title={c.title}>
            {#if onsort}
              <button type="button" class="sort" onclick={() => onsort(c.name)}>
                <span class="name">{c.name}</span>{#if sort?.column === c.name}<span class="arrow">{sort.desc ? '▼' : '▲'}</span>{/if}
                {#if c.detail}<span class="detail">{c.detail}</span>{/if}
              </button>
            {:else}
              <span class="plain"><span class="name">{c.name}</span>{#if c.detail}<span class="detail">{c.detail}</span>{/if}</span>
            {/if}
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#if adding}
        <tr class="draft">
          <td class="rownum">
            <button type="button" class="small primary" onclick={insert} disabled={saving}>Insert</button>
            <button type="button" class="small" onclick={() => oncanceladd?.()}>Cancel</button>
          </td>
          {#each columns as c}
            <td>
              {#if c.readonly}
                <span class="muted">generated</span>
              {:else}
                <div class="draftcell">
                  <input
                    class="mono"
                    bind:value={draft[c.name]}
                    disabled={draftNull[c.name]}
                    placeholder={draftNull[c.name] ? 'NULL' : 'default'}
                    onkeydown={draftKey}
                  />
                  <label class="null" title="Insert NULL"><input type="checkbox" bind:checked={draftNull[c.name]} />NULL</label>
                </div>
              {/if}
            </td>
          {/each}
        </tr>
      {/if}
      {#each rows as row, r}
        <tr class:selected={selected.includes(r)}>
          <td class="rownum">
            {#if onedit}
              <label><input type="checkbox" checked={selected.includes(r)} onchange={() => toggle(r)} />{offset + r + 1}</label>
            {:else}
              {offset + r + 1}
            {/if}
          </td>
          {#each row as v, c}
            {@const cell = cellText(v)}
            {#if editing && editing.row === r && editing.col === c}
              <td class="editing">
                <div class="editor">
                  <input
                    class="cell-editor mono"
                    bind:value={editing.text}
                    onkeydown={editorKey}
                    disabled={saving}
                  />
                  <button type="button" class="small" title="Set to NULL" onclick={() => commit(null)} disabled={saving}>NULL</button>
                  <button type="button" class="small primary" title="Save (Enter)" onclick={() => commit()} disabled={saving}>Save</button>
                </div>
              </td>
            {:else}
              <td
                class="mono {cell.kind}"
                class:ro={!onedit || columns[c].readonly || !editable(v)}
                title={cell.text.length > 40 ? cell.text : undefined}
                ondblclick={() => startEdit(r, c)}
              >{cell.text}</td>
            {/if}
          {/each}
        </tr>
      {:else}
        {#if !adding}
          <tr><td class="empty muted" colspan={columns.length + 1}>No rows</td></tr>
        {/if}
      {/each}
    </tbody>
  </table>
</div>

<style>
  .grid { overflow: auto; flex: 1; min-height: 0; border: 1px solid var(--border); border-radius: 6px; }
  table { border-collapse: collapse; font-size: 12px; min-width: 100%; }
  th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--hover);
    border-bottom: 1px solid var(--border);
    text-align: left;
    font-weight: 500;
    padding: 0;
    white-space: nowrap;
  }
  th .plain, button.sort { display: block; padding: 4px 8px; }
  button.sort { border: none; background: none; color: inherit; font: inherit; width: 100%; text-align: inherit; border-radius: 0; }
  .detail { display: block; color: var(--muted); font-weight: normal; font-size: 11px; }
  .arrow { margin-left: 4px; font-size: 10px; color: var(--accent); }
  td {
    padding: 3px 8px;
    border-bottom: 1px solid var(--border);
    border-right: 1px solid var(--border);
    max-width: 320px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12px;
    vertical-align: top;
  }
  td:not(.ro):not(.rownum):not(.editing) { cursor: text; }
  tbody tr:hover { background: var(--hover); }
  tr.selected { background: var(--selected); }
  .rownum { color: var(--muted); text-align: right; width: 1%; white-space: nowrap; background: var(--hover); }
  th.rownum { padding: 4px 8px; z-index: 2; }
  .rownum label { display: inline-flex; gap: 4px; align-items: center; margin: 0; }
  .null { color: var(--muted); font-style: italic; }
  .num { text-align: right; }
  .blob { color: var(--warn); }
  td.editing { padding: 1px 2px; max-width: none; overflow: visible; }
  .editor, .draftcell { display: flex; gap: 2px; align-items: center; }
  .editor input { min-width: 12em; flex: 1; padding: 1px 4px; font-size: 12px; }
  .draftcell input:not([type]) { min-width: 8em; flex: 1; padding: 1px 4px; font-size: 12px; }
  label.null { display: inline-flex; align-items: center; gap: 2px; margin: 0; font-size: 10px; font-style: normal; }
  .small { font-size: 11px; padding: 0 6px; }
  tr.draft td { background: var(--bg); border-bottom: 2px solid var(--accent); }
  .empty { text-align: center; padding: 16px; }
  .muted { color: var(--muted); }
</style>
