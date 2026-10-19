<script lang="ts" module>
  /** Payload of an internal drag, carried in dataTransfer under DRAG_TYPE. */
  export interface DragPayload {
    pane: number
    paths: string[]
  }
  export const DRAG_TYPE = 'application/x-xwebfm'
</script>

<script lang="ts">
  import type { Entry } from './api'
  import { humanSize, modeString, octal, parentPath } from './format'
  import type { PaneState, Row } from './pane.svelte'

  let {
    pane,
    index,
    active,
    onactivate,
    ondrop,
    onupload,
    onopen,
  }: {
    pane: PaneState
    index: number
    active: boolean
    onactivate: () => void
    /** Internal entries dropped on dir; copy/move are the modifier hints. */
    ondrop: (dir: string, payload: DragPayload, mods: { copy: boolean; move: boolean }) => void
    /** Files dragged in from the desktop. */
    onupload: (dir: string, files: File[]) => void
    /** Double-click on a row. */
    onopen: (row: Row) => void
  } = $props()

  const icon: Record<string, string> = { dir: '📁', symlink: '🔗', file: '📄' }
  const fmtDate = (s: string) => new Date(s).toLocaleString()

  /** Whether an entry's owner differs from the listed folder's owner. */
  function mismatch(e: Entry): boolean {
    const d = pane.dir
    return !pane.results && !!d && (e.uid !== d.uid || e.gid !== d.gid)
  }

  function ownerNote(e: Entry): string {
    const notes = []
    if (!e.user) notes.push(`uid ${e.uid} has no user account`)
    if (!e.group) notes.push(`gid ${e.gid} has no group`)
    const d = pane.dir
    if (mismatch(e) && d) notes.push(`folder is owned by ${d.user ?? d.uid}:${d.group ?? d.gid}`)
    return notes.join('; ')
  }

  /** A result's path relative to the search root. */
  function relative(p: string): string {
    const root = pane.results?.root ?? '/'
    return root === '/' ? p.slice(1) : p.slice(root.length + 1)
  }

  let dropTarget = $state<string | null>(null)
  let body: HTMLElement

  let crumbs = $derived(
    pane.path
      .split('/')
      .filter(Boolean)
      .map((name, i, parts) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') })),
  )

  // Keep the cursor row visible during keyboard navigation.
  $effect(() => {
    if (active) body?.querySelector(`[data-row="${pane.cursor}"]`)?.scrollIntoView({ block: 'nearest' })
  })

  function dragStart(ev: DragEvent, entry: Entry) {
    // Dragging a selected row drags the whole selection; otherwise just it.
    const paths = pane.selected.has(entry.path) ? [...pane.selected] : [entry.path]
    const payload: DragPayload = { pane: index, paths }
    ev.dataTransfer!.setData(DRAG_TYPE, JSON.stringify(payload))
    ev.dataTransfer!.setData('text/plain', paths.join('\n'))
    ev.dataTransfer!.effectAllowed = 'copyMove'
  }

  /** The directory a row accepts drops into, or null. */
  function rowTarget(row: Row): string | null {
    if (row.parent) return row.path
    return row.entry.type === 'dir' ? row.entry.path : null
  }

  function dragOver(ev: DragEvent, dir: string | null) {
    const types = ev.dataTransfer?.types ?? []
    if (!dir || !(types.includes(DRAG_TYPE) || types.includes('Files'))) return
    ev.preventDefault()
    ev.stopPropagation()
    dropTarget = dir
    if (types.includes('Files')) ev.dataTransfer!.dropEffect = 'copy'
    else if (ev.ctrlKey || ev.altKey) ev.dataTransfer!.dropEffect = 'copy'
    else if (ev.shiftKey) ev.dataTransfer!.dropEffect = 'move'
  }

  function drop(ev: DragEvent, dir: string | null) {
    if (!dir) return
    ev.preventDefault()
    ev.stopPropagation()
    dropTarget = null
    const dt = ev.dataTransfer!
    const raw = dt.getData(DRAG_TYPE)
    if (raw) {
      const payload = JSON.parse(raw) as DragPayload
      // Ignore drops of an entry onto itself or into the directory it's already in.
      if (payload.paths.some((p) => p === dir) || payload.paths.every((p) => parentPath(p) === dir)) return
      ondrop(dir, payload, { copy: ev.ctrlKey || ev.altKey, move: ev.shiftKey })
    } else if (dt.files.length) {
      onupload(dir, [...dt.files])
    }
  }
</script>

<!-- Keyboard navigation is handled globally in App; these handlers are for mouse focus and drops. -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
  class="pane"
  class:active
  class:droptarget={dropTarget === pane.path}
  onmousedown={onactivate}
  ondragover={(e) => dragOver(e, pane.path)}
  ondragleave={(e) => {
    if (!(e.currentTarget as Node).contains(e.relatedTarget as Node)) dropTarget = null
  }}
  ondrop={(e) => drop(e, pane.path)}
  aria-label="Pane {index + 1}"
>
  <header>
    {#if pane.results}
      <div class="results-bar">
        <button onclick={() => pane.navigate(pane.path)} title="Back to {pane.path}">← Back</button>
        <span>
          🔍 {pane.results.label}: {pane.resultEntries.length}{pane.results.truncated ? '+' : ''}
          result{pane.resultEntries.length === 1 ? '' : 's'} in <span class="mono">{pane.results.root}</span>
        </span>
      </div>
    {:else}
      <nav class="crumbs mono">
        <button class="link" onclick={() => pane.navigate('/')}>/</button>
        {#each crumbs as c, i}
          {#if i > 0}<span>/</span>{/if}
          <button class="link" onclick={() => pane.navigate(c.path)}>{c.name}</button>
        {/each}
      </nav>
    {/if}
    <span class="count">
      {#if pane.loading && !pane.stale}<span class="spinner small" aria-label="Refreshing"></span>{/if}
      {#if pane.selected.size}{pane.selected.size} selected{/if}
    </span>
    <button onclick={() => pane.load()} disabled={pane.loading} title="Refresh">⟳</button>
  </header>

  <div class="body" bind:this={body}>
    {#if pane.error}<p class="error">{pane.error}</p>{/if}
    {#if pane.stale && !pane.results}
      <div class="loading" role="status">
        <span class="spinner"></span> Loading <span class="mono">{pane.path}</span>…
      </div>
    {/if}
    <table class:hidden={pane.stale && !pane.results}>
      <thead>
        <tr>
          <th>Name</th>
          <th class="num">Size</th>
          <th>Mode</th>
          <th>Owner</th>
          <th class="date">Modified</th>
        </tr>
      </thead>
      <tbody>
        {#each pane.rows as row, i (row.parent ? '..' : row.entry.path)}
          {@const target = rowTarget(row)}
          {@const e = row.parent ? null : row.entry}
          <tr
            data-row={i}
            class:cursor={active && pane.cursor === i}
            class:selected={e && pane.selected.has(e.path)}
            class:droptarget={target !== null && dropTarget === target && target !== pane.path}
            draggable={e ? 'true' : 'false'}
            onclick={(ev) => pane.click(i, ev)}
            ondblclick={() => onopen(row)}
            ondragstart={(ev) => e && dragStart(ev, e)}
            ondragover={(ev) => dragOver(ev, target)}
            ondrop={(ev) => drop(ev, target)}
          >
            {#if e}
              <td class="name">
                <span aria-hidden="true">{icon[e.type] ?? '•'}</span>
                {pane.results ? relative(e.path) : e.name}{#if e.type === 'dir'}/{/if}
                {#if e.linkTarget}<span class="muted">→ {e.linkTarget}</span>{/if}
              </td>
              <td class="num">
                {#if e.type !== 'dir'}{humanSize(e.size)}{:else if pane.dirSizes.has(e.path)}<span
                    class="computed"
                    title="Total size from the last disk-usage scan">{humanSize(pane.dirSizes.get(e.path)!)}</span
                  >{/if}
              </td>
              <td class="mono" title={octal(e.mode) + (e.acl ? ' + ACL' : '')}>{modeString(e.type, e.mode)}{e.acl ? '+' : ''}</td>
              <td
                class="mono"
                class:mismatch={mismatch(e)}
                class:orphan={!e.user || !e.group}
                title={ownerNote(e)}
              >
                {e.user ?? e.uid}:{e.group ?? e.gid}
              </td>
              <td class="muted date">{fmtDate(e.mtime)}</td>
            {:else}
              <td class="name" colspan="5"><span aria-hidden="true">📁</span> ..</td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
    {#if pane.results && pane.resultEntries.length === 0}
      <p class="muted empty">No matches</p>
    {:else if !pane.results && !pane.stale && !pane.error && pane.entries.length === 0}
      <p class="muted empty">Empty directory — drop files here to upload</p>
    {/if}
  </div>
  {#if pane.fs && !pane.results}
    <footer class="fsinfo" title="{humanSize(pane.fs.free)} free including space reserved for root">
      {humanSize(pane.fs.avail)} free of {humanSize(pane.fs.total)} · {pane.fs.type}{pane.fs.reflink ? ' · reflinks' : ''}
    </footer>
  {/if}
</section>

<style>
  .pane {
    display: flex;
    flex-direction: column;
    min-width: 0;
    min-height: 0;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }
  .pane.active { border-color: var(--accent); }
  .pane.droptarget { outline: 2px dashed var(--accent); outline-offset: -2px; }
  header {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    border-bottom: 1px solid var(--border);
    background: var(--hover);
  }
  .crumbs { display: flex; flex-wrap: wrap; gap: 2px; align-items: center; color: var(--muted); flex: 1; min-width: 0; }
  .count { color: var(--muted); font-size: 12px; white-space: nowrap; }
  header button:not(.link) { padding: 0 8px; }
  .body { overflow: auto; flex: 1; }
  table { width: 100%; border-collapse: collapse; user-select: none; }
  th {
    text-align: left;
    font-weight: 500;
    color: var(--muted);
    padding: 4px 8px;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--bg);
  }
  td { padding: 3px 8px; white-space: nowrap; cursor: default; }
  tbody tr:hover { background: var(--hover); }
  tr.selected { background: var(--selected); }
  tr.selected:hover { background: var(--selected); }
  tr.cursor td { box-shadow: inset 0 1px var(--accent), inset 0 -1px var(--accent); }
  tr.droptarget td { background: var(--accent); color: #fff; }
  .num { text-align: right; }
  .name { width: 100%; white-space: normal; word-break: break-all; }
  .muted { color: var(--muted); }
  .empty { text-align: center; padding: 24px; }
  .hidden { display: none; }
  .results-bar { display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0; }
  .results-bar span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .results-bar button { padding: 0 8px; }
  .computed { font-style: italic; color: var(--muted); }
  td.mismatch { color: var(--warn); }
  td.orphan { color: var(--danger); }
  .fsinfo {
    border-top: 1px solid var(--border);
    padding: 3px 10px;
    font-size: 12px;
    color: var(--muted);
    background: var(--hover);
  }
  /* Fades in after a short delay so fast loads don't flash. */
  .loading {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 24px 12px;
    color: var(--muted);
    justify-content: center;
    animation: appear 0.2s 0.15s both;
  }
  @keyframes appear {
    from { opacity: 0; }
    to { opacity: 1; }
  }
  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
    display: inline-block;
    vertical-align: middle;
  }
  .spinner.small { width: 10px; height: 10px; border-width: 2px; margin-right: 4px; }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  .error { padding: 0 10px; }
  @media (max-width: 1100px) {
    .date { display: none; }
  }
  button.link { border: none; background: none; padding: 0 2px; color: var(--accent); font: inherit; }
  button.link:hover { text-decoration: underline; }
</style>
