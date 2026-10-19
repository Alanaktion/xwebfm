<script lang="ts">
  import { api, type Job } from './api'
  import { humanSize } from './format'
  import { isFinished, type JobsStore } from './jobs.svelte'

  let { store, onclose }: { store: JobsStore; onclose: () => void } = $props()

  let jobs = $derived([...store.jobs].reverse())
  let anyFinished = $derived(store.jobs.some((j) => isFinished(j.state)))

  const stateLabel: Record<Job['state'], string> = {
    queued: 'Queued',
    running: 'Running',
    done: 'Done',
    failed: 'Failed',
    cancelled: 'Cancelled',
  }

  /** Fraction complete, by bytes when the job moves data, else by entries. */
  function fraction(j: Job): number | null {
    const p = j.progress
    if (j.state === 'done') return 1
    if (p.totalBytes > 0) return Math.min(p.bytes / p.totalBytes, 1)
    if (p.totalEntries > 0) return Math.min(p.entries / p.totalEntries, 1)
    return null
  }

  function duration(seconds: number): string {
    if (!isFinite(seconds) || seconds < 0) return ''
    if (seconds < 60) return `${Math.ceil(seconds)}s`
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`
    return `${Math.floor(seconds / 3600)}h ${Math.round((seconds % 3600) / 60)}m`
  }

  function detail(j: Job): string {
    const p = j.progress
    const parts: string[] = []
    if (p.totalBytes > 0) parts.push(`${humanSize(p.bytes)} of ${humanSize(p.totalBytes)}`)
    else if (p.bytes > 0) parts.push(humanSize(p.bytes))
    if (p.totalEntries > 0) parts.push(`${p.entries} of ${p.totalEntries} items`)
    else if (p.entries > 0) parts.push(`${p.entries} items`)
    const rate = store.rates.get(j.id)
    if (j.state === 'running' && rate && rate > 0) {
      parts.push(`${humanSize(rate)}/s`)
      if (p.totalBytes > p.bytes) parts.push(`${duration((p.totalBytes - p.bytes) / rate)} left`)
    }
    if (isFinished(j.state) && j.started && j.finished) {
      parts.push(`took ${duration((Date.parse(j.finished) - Date.parse(j.started)) / 1000) || '0s'}`)
    }
    return parts.join(' · ')
  }

  /** Keeps the end of long paths, where the file name is. */
  const tail = (p: string, max = 90) => (p.length > max ? '…' + p.slice(-(max - 1)) : p)

  async function act(fn: () => Promise<unknown>) {
    try {
      await fn()
    } catch (e) {
      alert((e as Error).message)
    }
  }
</script>

<section class="jobs" aria-label="Jobs">
  <header>
    <strong>Jobs</strong>
    {#if !store.connected}<span class="warn">reconnecting…</span>{/if}
    <span class="spacer"></span>
    <button onclick={() => act(api.jobs.clear)} disabled={!anyFinished}>Clear finished</button>
    <button onclick={onclose} aria-label="Close jobs panel">✕</button>
  </header>
  <ul>
    {#each jobs as j (j.id)}
      {@const f = fraction(j)}
      <li class={j.state}>
        <div class="row">
          <span class="badge">{stateLabel[j.state]}</span>
          <span class="title" title={j.title}>{j.title}</span>
          {#if !isFinished(j.state)}
            <button onclick={() => act(() => api.jobs.cancel(j.id))}>Cancel</button>
          {:else}
            <button onclick={() => act(() => api.jobs.dismiss(j.id))} aria-label="Dismiss">✕</button>
          {/if}
        </div>
        {#if j.state === 'running' || j.state === 'queued'}
          <progress max="1" value={f ?? undefined}></progress>
        {/if}
        <div class="detail">{detail(j)}</div>
        {#if j.state === 'running' && j.progress.current}
          <div class="current mono" title={j.progress.current}>{tail(j.progress.current)}</div>
        {/if}
        {#if j.state === 'cancelled' && (j.kind === 'delete' || j.kind === 'chown' || j.kind === 'chmod') && j.started}
          <div class="detail">Stopped part-way; changes already made remain.</div>
        {/if}
        {#each j.errors ?? [] as err}<div class="error">{err}</div>{/each}
      </li>
    {:else}
      <li class="empty">No jobs. Copies, moves, deletes and recursive owner/mode changes run here.</li>
    {/each}
  </ul>
</section>

<style>
  .jobs {
    border-top: 1px solid var(--border);
    max-height: 40vh;
    display: flex;
    flex-direction: column;
    background: var(--bg);
  }
  header { display: flex; align-items: center; gap: 8px; padding: 6px 12px; border-bottom: 1px solid var(--border); }
  header button { padding: 2px 8px; font-size: 12px; }
  .spacer { flex: 1; }
  .warn { color: var(--danger); font-size: 12px; }
  ul { list-style: none; margin: 0; padding: 0; overflow: auto; }
  li { padding: 8px 12px; border-bottom: 1px solid var(--border); }
  .row { display: flex; align-items: center; gap: 8px; }
  .row button { padding: 1px 8px; font-size: 12px; }
  .title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .badge {
    font-size: 11px;
    padding: 1px 6px;
    border-radius: 4px;
    background: var(--hover);
    color: var(--muted);
  }
  .running .badge { background: var(--accent); color: #fff; }
  .done .badge { color: var(--ok); }
  .failed .badge { background: var(--danger); color: #fff; }
  progress { width: 100%; height: 6px; margin-top: 6px; accent-color: var(--accent); }
  .detail { color: var(--muted); font-size: 12px; margin-top: 4px; }
  .current { color: var(--muted); font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .error { color: var(--danger); font-size: 12px; margin-top: 4px; word-break: break-all; }
  .empty { color: var(--muted); text-align: center; padding: 16px; }
</style>
