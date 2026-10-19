<script lang="ts">
  import { onMount } from 'svelte'
  import { api, ApiError, type Account, type Entry, type FindResult, type Job, type UsageReport } from './lib/api'
  import { archiveInfo, humanSize, parentPath } from './lib/format'
  import ExtractDialog from './lib/ExtractDialog.svelte'
  import ArchiveDialog from './lib/ArchiveDialog.svelte'
  import UsageDialog from './lib/UsageDialog.svelte'
  import FindDialog from './lib/FindDialog.svelte'
  import FixOwnerDialog from './lib/FixOwnerDialog.svelte'
  import PropertiesDialog from './lib/PropertiesDialog.svelte'
  import LinkDialog from './lib/LinkDialog.svelte'
  import ViewerDialog from './lib/ViewerDialog.svelte'
  import Sidebar from './lib/Sidebar.svelte'
  import ColumnsDialog from './lib/ColumnsDialog.svelte'
  import ShortcutsDialog from './lib/ShortcutsDialog.svelte'
  import { persist, settings } from './lib/settings.svelte'
  import { compareStatus } from './lib/pane.svelte'
  import { isFinished, JobsStore } from './lib/jobs.svelte'
  import JobsPanel from './lib/JobsPanel.svelte'
  import { PaneState, type Row } from './lib/pane.svelte'
  import Pane, { type DragPayload } from './lib/Pane.svelte'
  import ChownDialog from './lib/ChownDialog.svelte'
  import ChmodDialog from './lib/ChmodDialog.svelte'
  import TransferDialog from './lib/TransferDialog.svelte'
  import RenameDialog from './lib/RenameDialog.svelte'
  import MkdirDialog from './lib/MkdirDialog.svelte'
  import DeleteDialog from './lib/DeleteDialog.svelte'

  type Dialog =
    | { kind: 'transfer'; op: 'copy' | 'move'; sources: Entry[]; dest: string }
    | { kind: 'rename'; entry: Entry }
    | { kind: 'mkdir'; dir: string }
    | { kind: 'delete'; entries: Entry[] }
    | { kind: 'chown'; entries: Entry[] }
    | { kind: 'chmod'; entries: Entry[] }
    | { kind: 'extract'; archives: Entry[] }
    | { kind: 'archive'; sources: Entry[] }
    | { kind: 'usage'; path: string }
    | { kind: 'fixowner'; entries: Entry[] }
    | { kind: 'props'; path: string }
    | { kind: 'link'; entries: Entry[] }
    | { kind: 'view'; entry: Entry; edit: boolean }
    | { kind: 'columns' }
    | { kind: 'shortcuts' }
    | { kind: 'find'; root: string; rootOwner: string }

  // Both pane directories live in the URL hash (#l=/a&r=/b) so reloads keep them.
  const hash = new URLSearchParams(location.hash.slice(1))
  const panes = [new PaneState(hash.get('l') || '/', 'pane0'), new PaneState(hash.get('r') || '/', 'pane1')]
  let comparing = $state(false)
  let active = $state(0)
  let dialog = $state<Dialog | null>(null)
  let status = $state<{ text: string; error: boolean } | null>(null)
  let users = $state<Account[]>([])
  let groups = $state<Account[]>([])
  let uploadInput: HTMLInputElement
  let jobsOpen = $state(false)
  let createFormats = $state<string[]>([])

  const jobs = new JobsStore({
    onchanged: (dirs) => {
      for (const p of panes) if (!dirs || dirs.includes(p.path)) p.load()
    },
    onfinished: (job) => {
      const p = job.progress
      const summary =
        job.state === 'done'
          ? `done${p.entries ? ` · ${p.entries} items` : ''}${p.bytes ? ` · ${humanSize(p.bytes)}` : ''}`
          : job.state === 'failed'
            ? `failed: ${job.errors?.[0] ?? 'unknown error'}${(job.errors?.length ?? 0) > 1 ? ` (+${job.errors!.length - 1} more)` : ''}`
            : 'cancelled'
      status = { text: `${job.title}: ${summary}`, error: job.state === 'failed' }
    },
  })

  let pane = $derived(panes[active])
  let other = $derived(panes[1 - active])
  let targets = $derived(pane.targets())
  let archives = $derived(targets.filter((t) => t.type === 'file' && archiveInfo(t.name)))

  $effect(() => {
    const params = new URLSearchParams({ l: panes[0].path, r: panes[1].path })
    history.replaceState(null, '', `#${params}`)
  })

  onMount(() => {
    jobs.connect()
    panes.forEach((p) => p.load())
    api.info().then((i) => (createFormats = i.archive.create)).catch(() => {})
    api.users().then((u) => (users = u)).catch(() => {})
    api.groups().then((g) => (groups = g)).catch(() => {})
    return () => jobs.close()
  })

  /** Opens the owner or mode dialog for one path (from Properties). */
  async function openFor(path: string, kind: 'chown' | 'chmod') {
    const entry = await api.stat(path)
    dialog = { kind, entries: [entry] }
  }

  /** Shows folder totals from a disk-usage report in both panes. */
  function showSizes(report: UsageReport) {
    for (const p of panes) {
      p.dirSizes.set(report.path, report.size)
      for (const c of report.children) if (c.type === 'dir') p.dirSizes.set(c.path, c.size)
    }
  }

  /** Waits for a search job and shows its results in the pane it started from. */
  async function startFind(job: Job, label: string) {
    const target = pane
    dialog = null
    status = { text: `Searching ${job.items[0].src}…`, error: false }
    const done = await jobs.waitFor(job.id)
    if (!done.hasResult) return // cancelled or failed; the job panel says why
    const res = await api.jobs.result<FindResult>(done.id)
    target.showResults(res, label)
    status = {
      text: `${res.entries.length}${res.truncated ? '+' : ''} matches among ${res.scanned.toLocaleString()} items`,
      error: false,
    }
    api.jobs.dismiss(done.id).catch(() => {})
  }

  function queued(job: Job) {
    dialog = null
    jobsOpen = true
    // A quick job may already have finished (over the WebSocket) before the
    // submit response arrived; don't replace its result with "Started".
    const known = jobs.jobs.find((j) => j.id === job.id)
    if (!known || !isFinished(known.state)) status = { text: `Started: ${job.title}`, error: false }
  }

  function done(text: string, error = false) {
    if (!error) dialog = null
    status = { text, error }
    panes.forEach((p) => p.load())
  }

  const actions = {
    rename: () => targets.length === 1 && (dialog = { kind: 'rename', entry: targets[0] }),
    download: () => {
      for (const t of targets) {
        // Separate anchors so multiple downloads aren't collapsed into one navigation.
        const a = document.createElement('a')
        a.href = api.downloadUrl(t.path)
        a.download = ''
        a.click()
      }
    },
    copy: () => targets.length && (dialog = { kind: 'transfer', op: 'copy', sources: targets, dest: other.path }),
    move: () => targets.length && (dialog = { kind: 'transfer', op: 'move', sources: targets, dest: other.path }),
    mkdir: () => (dialog = { kind: 'mkdir', dir: pane.path }),
    delete: () => targets.length && (dialog = { kind: 'delete', entries: targets }),
    chown: () => targets.length && (dialog = { kind: 'chown', entries: targets }),
    chmod: () => {
      const entries = targets.filter((t) => t.type !== 'symlink')
      if (entries.length) dialog = { kind: 'chmod', entries }
    },
    upload: () => uploadInput.click(),
    extract: () => archives.length && (dialog = { kind: 'extract', archives }),
    usage: () => (dialog = { kind: 'usage', path: pane.path }),
    fixowner: () => targets.length && (dialog = { kind: 'fixowner', entries: targets }),
    props: () => {
      const t = targets[0] ?? pane.dir
      if (t) dialog = { kind: 'props', path: t.path }
    },
    link: () => targets.length && (dialog = { kind: 'link', entries: targets }),
    view: (edit = false) => {
      const t = targets.length === 1 ? targets[0] : undefined
      if (t?.type === 'file') dialog = { kind: 'view', entry: t, edit }
    },
    toggleHidden: () => {
      settings.showHidden = !settings.showHidden
      persist('showHidden', settings.showHidden)
    },
    toggleSidebar: () => {
      settings.sidebar = !settings.sidebar
      persist('sidebar', settings.sidebar)
    },
    compare: () => (comparing = !comparing),
    /** Selects, in both panes, entries missing from or differing in the other. */
    selectDifferences: () => {
      for (const [i, p] of panes.entries()) {
        const others = new Map(panes[1 - i].entries.map((e) => [e.name, e]))
        p.selected.clear()
        for (const e of p.visible) if (compareStatus(e, others.get(e.name)) !== 'same') p.selected.add(e.path)
      }
    },
    find: () =>
      (dialog = {
        kind: 'find',
        root: pane.path,
        rootOwner: pane.dir ? (pane.dir.user ?? String(pane.dir.uid)) : 'its owner',
      }),
    archive: () => targets.length && (dialog = { kind: 'archive', sources: targets }),
  }

  /** Enter or double-click: archives open the extract dialog. */
  function openRow(p: PaneState, row: Row | undefined) {
    if (row && !row.parent && row.entry.type === 'file' && archiveInfo(row.entry.name)) {
      dialog = { kind: 'extract', archives: [row.entry] }
      return
    }
    p.open(row)
  }

  function onKey(ev: KeyboardEvent) {
    if (dialog) return
    const el = ev.target as HTMLElement
    if (el.closest('input, select, textarea')) return
    const ctrl = ev.ctrlKey || ev.metaKey
    const mode = ev.shiftKey ? 'extend' : 'plain'
    const keys: Record<string, () => unknown> = {
      Tab: () => (active = 1 - active),
      ArrowDown: () => pane.move(1, mode),
      ArrowUp: () => pane.move(-1, mode),
      PageDown: () => pane.move(20, mode),
      PageUp: () => pane.move(-20, mode),
      Home: () => pane.move(-Infinity, mode),
      End: () => pane.move(Infinity, mode),
      Enter: () => openRow(pane, pane.rows[pane.cursor]),
      Backspace: () => (pane.results ? pane.navigate(pane.path) : pane.path !== '/' && pane.navigate(parentPath(pane.path))),
      Escape: () => pane.selected.clear(),
      Insert: toggleCursor,
      ' ': toggleCursor,
      F2: actions.rename,
      F3: () => (ev.shiftKey ? actions.download() : actions.view()),
      F4: () => actions.view(true),
      '/': () => pane.focusFilter++,
      F5: actions.copy,
      F6: actions.move,
      F7: actions.mkdir,
      F8: actions.delete,
      Delete: actions.delete,
    }
    if (ctrl && ev.key === 'a') {
      ev.preventDefault()
      pane.selectAll()
      return
    }
    if ((ctrl && ev.key === '/') || (!ctrl && ev.key === '?')) {
      ev.preventDefault()
      dialog = { kind: 'shortcuts' }
      return
    }
    if (ctrl && (ev.key === 'l' || ev.key === 'h' || ev.key === '=')) {
      ev.preventDefault()
      if (ev.key === 'l') pane.editingPath = true
      else if (ev.key === 'h') actions.toggleHidden()
      else actions.compare()
      return
    }
    // Typing a name filters the active pane.
    if (!ctrl && !ev.altKey && ev.key.length === 1 && /[\p{L}\p{N}._-]/u.test(ev.key)) {
      ev.preventDefault()
      pane.filter += ev.key
      pane.focusFilter++
      return
    }
    if (ctrl && ev.key === 'u') {
      ev.preventDefault()
      actions.usage()
      return
    }
    if (ev.altKey && ev.key === 'Enter') {
      ev.preventDefault()
      actions.props()
      return
    }
    if (ev.altKey && ev.key === 'F7') {
      ev.preventDefault()
      actions.find()
      return
    }
    if (ctrl && ev.key === 'r') {
      ev.preventDefault()
      panes.forEach((p) => p.load())
      return
    }
    const fn = !ctrl && !ev.altKey ? keys[ev.key] : undefined
    if (fn) {
      ev.preventDefault()
      fn()
    }
  }

  function toggleCursor() {
    const row = pane.rows[pane.cursor]
    if (row && !row.parent) pane.toggle(row.entry.path)
    pane.move(1, 'keep')
  }

  function onDrop(target: number, dir: string, payload: DragPayload, mods: { copy: boolean; move: boolean }) {
    const from = panes[payload.pane]
    const sources = from.entries.filter((e) => payload.paths.includes(e.path))
    if (!sources.length) return
    // Within a pane a drag moves; between panes it copies (like most file
    // managers). Ctrl/Alt forces copy and Shift forces move.
    const op = mods.copy ? 'copy' : mods.move ? 'move' : payload.pane === target ? 'move' : 'copy'
    active = target
    dialog = { kind: 'transfer', op, sources, dest: dir }
  }

  async function upload(dir: string, files: File[]) {
    let ok = 0
    const failures: string[] = []
    for (const [i, file] of files.entries()) {
      status = { text: `Uploading ${i + 1}/${files.length}: ${file.name}`, error: false }
      try {
        await api.upload(dir, file).catch((e) => {
          if (e instanceof ApiError && e.status === 409 && confirm(`${file.name} already exists in ${dir}. Replace it?`)) {
            return api.upload(dir, file, true)
          }
          throw e
        })
        ok++
      } catch (e) {
        failures.push(`${file.name}: ${(e as Error).message}`)
      }
    }
    done(failures.length ? failures.join('; ') : `Uploaded ${ok} file${ok === 1 ? '' : 's'} to ${dir}`, failures.length > 0)
  }
</script>

<svelte:window onkeydown={onKey} />

<div class="app">
  <div class="main" class:with-sidebar={settings.sidebar}>
    {#if settings.sidebar}
      <Sidebar {users} current={pane.path} onnavigate={(p) => pane.navigate(p)} />
    {/if}
    <div class="panes">
      {#each panes as p, i}
        <Pane
          compareWith={comparing ? panes[1 - i] : null}
          pane={p}
          index={i}
          active={active === i}
          onactivate={() => (active = i)}
          ondrop={(dir, payload, mods) => onDrop(i, dir, payload, mods)}
          onupload={upload}
          onopen={(row) => openRow(p, row)}
          oncolumns={() => (dialog = { kind: 'columns' })}
        />
      {/each}
    </div>
  </div>

  {#if jobsOpen}<JobsPanel store={jobs} onclose={() => (jobsOpen = false)} />{/if}

  <footer>
    <div class="status" class:error={status?.error} title={status?.text}>
      {#if status}{status.text}{:else}<span class="muted">Tab switches panes · Space/Insert selects · drag to copy or move · Ctrl+/ for all shortcuts</span>{/if}
    </div>
    <div class="keys">
      <button onclick={actions.rename} disabled={targets.length !== 1}><kbd>F2</kbd>Rename</button>
      <button onclick={() => actions.view()} disabled={targets.length !== 1 || targets[0].type !== 'file'}><kbd>F3</kbd>View</button>
      <button onclick={() => actions.view(true)} disabled={targets.length !== 1 || targets[0].type !== 'file'}><kbd>F4</kbd>Edit</button>
      <button onclick={actions.download} disabled={!targets.length} title="Shift+F3">Download</button>
      <button onclick={actions.copy} disabled={!targets.length}><kbd>F5</kbd>Copy</button>
      <button onclick={actions.move} disabled={!targets.length}><kbd>F6</kbd>Move</button>
      <button onclick={actions.mkdir}><kbd>F7</kbd>Mkdir</button>
      <button onclick={actions.delete} disabled={!targets.length}><kbd>F8</kbd>Delete</button>
      <button onclick={actions.chown} disabled={!targets.length}>Owner</button>
      <button onclick={actions.chmod} disabled={!targets.some((t) => t.type !== 'symlink')}>Mode</button>
      <button onclick={actions.fixowner} disabled={!targets.some((t) => t.type === 'dir')} title="Recursively unify ownership">Fix owner</button>
      <button onclick={actions.props} title="Alt+Enter">Properties</button>
      <button onclick={actions.link} disabled={!targets.length}>Link</button>
      <button onclick={actions.upload}>Upload</button>
      <button onclick={actions.extract} disabled={!archives.length}>Extract</button>
      <button onclick={actions.archive} disabled={!targets.length || !createFormats.length}>Archive</button>
      <button onclick={actions.find} title="Alt+F7">Find</button>
      <button onclick={actions.usage} title="Ctrl+U">Usage</button>
      <span class="spacer"></span>
      <button class:on={settings.sidebar} onclick={actions.toggleSidebar} title="Show places">Places</button>
      <button onclick={() => (dialog = { kind: 'columns' })} title="Choose columns (or right-click a column header)">Columns</button>
      <button class:on={settings.showHidden} onclick={actions.toggleHidden} title="Ctrl+H">Hidden</button>
      <button class:on={comparing} onclick={actions.compare} title="Ctrl+= — highlight differences between the panes">Compare</button>
      {#if comparing}<button onclick={actions.selectDifferences}>Select differences</button>{/if}
      <button class="jobs-toggle" class:on={jobsOpen} onclick={() => (jobsOpen = !jobsOpen)} aria-expanded={jobsOpen}>
        Jobs
        {#if jobs.active}<span class="count">{jobs.active}</span>{/if}
        {#if !jobs.connected}<span class="offline" title="Live updates disconnected; reconnecting">●</span>{/if}
      </button>
      <button onclick={() => (dialog = { kind: 'shortcuts' })} title="Keyboard shortcuts (Ctrl+/)" aria-label="Keyboard shortcuts">?</button>
    </div>
  </footer>
</div>

<input
  type="file"
  multiple
  hidden
  bind:this={uploadInput}
  onchange={() => {
    const files = [...(uploadInput.files ?? [])]
    uploadInput.value = ''
    if (files.length) upload(pane.path, files)
  }}
/>

{#if dialog?.kind === 'transfer'}
  <TransferDialog {...dialog} {users} {groups} onclose={() => (dialog = null)} onqueued={queued} />
{:else if dialog?.kind === 'rename'}
  <RenameDialog entry={dialog.entry} onclose={() => (dialog = null)} ondone={done} />
{:else if dialog?.kind === 'mkdir'}
  <MkdirDialog dir={dialog.dir} {users} {groups} onclose={() => (dialog = null)} ondone={done} />
{:else if dialog?.kind === 'delete'}
  <DeleteDialog entries={dialog.entries} onclose={() => (dialog = null)} onqueued={queued} />
{:else if dialog?.kind === 'chown'}
  <ChownDialog
    entries={dialog.entries}
    {users}
    {groups}
    onclose={() => (dialog = null)}
    ondone={done}
    onqueued={queued}
  />
{:else if dialog?.kind === 'extract'}
  <ExtractDialog
    archives={dialog.archives}
    dest={pane.path}
    otherDir={other.path}
    {users}
    {groups}
    onclose={() => (dialog = null)}
    onqueued={queued}
  />
{:else if dialog?.kind === 'archive'}
  <ArchiveDialog
    sources={dialog.sources}
    dir={pane.path}
    formats={createFormats}
    {users}
    {groups}
    onclose={() => (dialog = null)}
    onqueued={queued}
  />
{:else if dialog?.kind === 'columns'}
  <ColumnsDialog onclose={() => (dialog = null)} />
{:else if dialog?.kind === 'shortcuts'}
  <ShortcutsDialog onclose={() => (dialog = null)} />
{:else if dialog?.kind === 'view'}
  <ViewerDialog entry={dialog.entry} edit={dialog.edit} onclose={() => (dialog = null)} ondone={(msg) => {
    status = { text: msg, error: false }
    panes.forEach((p) => p.load())
  }} />
{:else if dialog?.kind === 'fixowner'}
  <FixOwnerDialog entries={dialog.entries} dir={pane.dir} onclose={() => (dialog = null)} onqueued={queued} />
{:else if dialog?.kind === 'link'}
  <LinkDialog entries={dialog.entries} dest={other.path} onclose={() => (dialog = null)} ondone={done} />
{:else if dialog?.kind === 'props'}
  {@const path = dialog.path}
  <PropertiesDialog
    {path}
    {users}
    {groups}
    onclose={() => (dialog = null)}
    onqueued={queued}
    onchanged={() => panes.forEach((p) => p.load())}
    onowner={() => openFor(path, 'chown')}
    onmode={() => openFor(path, 'chmod')}
  />
{:else if dialog?.kind === 'usage'}
  <UsageDialog
    path={dialog.path}
    store={jobs}
    onclose={() => (dialog = null)}
    onreport={showSizes}
    onopen={(p) => {
      dialog = null
      pane.navigate(p)
    }}
  />
{:else if dialog?.kind === 'find'}
  <FindDialog
    root={dialog.root}
    rootOwner={dialog.rootOwner}
    {users}
    {groups}
    onclose={() => (dialog = null)}
    onstart={startFind}
  />
{:else if dialog?.kind === 'chmod'}
  <ChmodDialog entries={dialog.entries} onclose={() => (dialog = null)} ondone={done} onqueued={queued} />
{/if}

<style>
  .app { display: flex; flex-direction: column; height: 100vh; height: 100dvh; }
  .main { flex: 1; min-height: 0; display: grid; grid-template-columns: 1fr; }
  .main.with-sidebar { grid-template-columns: 180px 1fr; padding-left: 8px; }
  .main.with-sidebar > :global(.sidebar) { margin: 8px 0; }
  @media (max-width: 800px) {
    .main.with-sidebar { grid-template-columns: 1fr; padding-left: 0; }
    .main.with-sidebar > :global(.sidebar) { display: none; }
  }
  footer button.on { border-color: var(--accent); color: var(--accent); }
  .panes {
    flex: 1;
    min-height: 0;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
    padding: 8px;
  }
  @media (max-width: 800px) {
    .panes { grid-template-columns: 1fr; grid-template-rows: 1fr 1fr; }
  }
  footer { border-top: 1px solid var(--border); padding: 6px 8px; }
  .status { padding: 2px 4px 6px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--ok); }
  .status.error { color: var(--danger); }
  .muted { color: var(--muted); }
  .keys { display: flex; flex-wrap: wrap; gap: 4px; }
  .keys button { display: inline-flex; gap: 4px; align-items: center; padding: 3px 8px; }
  kbd { font: 11px ui-monospace, monospace; color: var(--muted); }
  .spacer { flex: 1; }
  .jobs-toggle.on { border-color: var(--accent); }
  .count {
    background: var(--accent);
    color: #fff;
    border-radius: 9px;
    padding: 0 6px;
    font-size: 11px;
    min-width: 18px;
    text-align: center;
  }
  .offline { color: var(--danger); font-size: 10px; }
</style>
