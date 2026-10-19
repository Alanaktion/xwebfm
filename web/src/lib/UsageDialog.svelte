<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api, type UsageReport } from './api'
  import { humanSize } from './format'
  import type { JobsStore } from './jobs.svelte'
  import Modal from './Modal.svelte'

  let {
    path: startPath,
    store,
    onclose,
    onreport,
    onopen,
  }: {
    path: string
    store: JobsStore
    onclose: () => void
    /** Called with every finished report (to show folder sizes in panes). */
    onreport: (report: UsageReport) => void
    /** Opens a folder in the active pane. */
    onopen: (path: string) => void
  } = $props()

  const cache = new Map<string, UsageReport>()
  let trail = $state<string[]>(untrack(() => [startPath]))
  let report = $state<UsageReport | null>(null)
  let jobId = $state<string | null>(null)
  let error = $state('')

  let current = $derived(trail[trail.length - 1])
  let job = $derived(jobId ? store.jobs.find((j) => j.id === jobId) : undefined)
  let maxChild = $derived(Math.max(1, ...(report?.children.map((c) => c.disk) ?? [])))
  let maxOwner = $derived(Math.max(1, ...(report?.byOwner.map((o) => o.disk) ?? [])))

  onMount(() => {
    scan(current)
    return () => {
      if (jobId) api.jobs.cancel(jobId).catch(() => {})
    }
  })

  async function scan(path: string, fresh = false) {
    error = ''
    if (!fresh && cache.has(path)) {
      report = cache.get(path)!
      return
    }
    report = null
    try {
      const started = await api.jobs.submit({ kind: 'usage', paths: [path] })
      jobId = started.id
      const done = await store.waitFor(started.id)
      if (jobId !== started.id) return // superseded by another scan
      jobId = null
      if (done.state !== 'done') {
        error = done.state === 'cancelled' ? 'Scan cancelled' : (done.errors?.[0] ?? 'Scan failed')
        return
      }
      const r = await api.jobs.result<UsageReport>(done.id)
      cache.set(path, r)
      onreport(r)
      if (current === path) report = r
      api.jobs.dismiss(done.id).catch(() => {})
    } catch (e) {
      error = (e as Error).message
    }
  }

  function drill(path: string) {
    trail = [...trail, path]
    scan(path)
  }

  function back(i: number) {
    trail = trail.slice(0, i + 1)
    scan(trail[i])
  }

  const pct = (n: number, max: number) => `${Math.max(0.5, (n / max) * 100)}%`
</script>

<Modal title="Disk usage" wide {onclose}>
  <nav class="trail mono">
    {#each trail as p, i}
      {#if i > 0}<span class="muted">›</span>{/if}
      <button class="link" disabled={i === trail.length - 1} onclick={() => back(i)}>{i === 0 ? p : p.slice(p.lastIndexOf('/') + 1)}</button>
    {/each}
    <span class="spacer"></span>
    {#if report}<button onclick={() => scan(current, true)}>Rescan</button>{/if}
    <button onclick={() => onopen(current)}>Open folder</button>
  </nav>

  {#if error}
    <p class="error">{error}</p>
  {:else if !report}
    <div class="scanning">
      <span class="spinner"></span>
      Scanning… {job ? `${job.progress.entries.toLocaleString()} items, ${humanSize(job.progress.bytes)}` : ''}
      {#if jobId}<button onclick={() => jobId && api.jobs.cancel(jobId)}>Cancel</button>{/if}
    </div>
  {:else}
    <p class="summary">
      <strong>{humanSize(report.disk)}</strong> on disk ({humanSize(report.size)} apparent) ·
      {report.entries.toLocaleString()} items · stays on this filesystem; hard links counted once
    </p>
    <div class="cols">
      <section>
        <h3>Contents</h3>
        <ul class="bars">
          {#each report.children as c (c.path)}
            <li>
              <div class="bar" style:width={pct(c.disk, maxChild)}></div>
              <span class="label">
                {#if c.type === 'dir'}
                  <button class="link" onclick={() => drill(c.path)}>📁 {c.name}/</button>
                {:else}
                  <span>{c.name}</span>
                {/if}
              </span>
              <span class="num">{humanSize(c.disk)}</span>
              <span class="num muted">{c.entries.toLocaleString()}</span>
            </li>
          {:else}
            <li class="muted">Empty</li>
          {/each}
        </ul>
      </section>
      <section>
        <h3>By owner</h3>
        <ul class="bars">
          {#each report.byOwner as o (o.uid)}
            <li>
              <div class="bar owner" style:width={pct(o.disk, maxOwner)}></div>
              <span class="label" class:orphan={!o.user}>{o.user ?? `uid ${o.uid} (no such user)`}</span>
              <span class="num">{humanSize(o.disk)}</span>
              <span class="num muted">{o.entries.toLocaleString()}</span>
            </li>
          {/each}
        </ul>
      </section>
    </div>
  {/if}
</Modal>

<style>
  .trail { display: flex; flex-wrap: wrap; gap: 4px; align-items: center; }
  .trail button:not(.link) { padding: 1px 8px; font-size: 12px; }
  .spacer { flex: 1; }
  .summary { color: var(--muted); font-size: 13px; }
  .cols { display: grid; grid-template-columns: 3fr 2fr; gap: 16px; }
  @media (max-width: 640px) {
    .cols { grid-template-columns: 1fr; }
  }
  h3 { font-size: 13px; margin: 8px 0 4px; color: var(--muted); font-weight: 500; }
  .bars { list-style: none; margin: 0; padding: 0; max-height: 50vh; overflow: auto; }
  .bars li {
    position: relative;
    display: grid;
    grid-template-columns: 1fr auto 4.5em;
    gap: 8px;
    padding: 3px 6px;
    align-items: center;
  }
  .bar {
    position: absolute;
    inset: 2px auto 2px 0;
    background: color-mix(in srgb, var(--accent) 18%, transparent);
    border-radius: 3px;
    z-index: 0;
  }
  .bar.owner { background: color-mix(in srgb, var(--ok) 20%, transparent); }
  .label, .num { position: relative; z-index: 1; }
  .label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .num { text-align: right; font-variant-numeric: tabular-nums; }
  .muted { color: var(--muted); }
  .orphan { color: var(--danger); }
  .scanning { display: flex; align-items: center; gap: 10px; padding: 24px 0; color: var(--muted); }
  .scanning button { padding: 1px 8px; }
  .spinner {
    width: 14px;
    height: 14px;
    border: 2px solid var(--border);
    border-top-color: var(--accent);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  button.link { border: none; background: none; padding: 0 2px; color: var(--accent); font: inherit; text-align: left; }
  button.link:disabled { color: var(--fg); cursor: default; }
</style>
