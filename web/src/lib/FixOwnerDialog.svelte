<script lang="ts">
  import { api, type Entry, type Job } from './api'
  import Modal from './Modal.svelte'

  let {
    entries,
    dir,
    onclose,
    onqueued,
  }: {
    entries: Entry[]
    /** The folder containing the entries (its owner is the "parent" choice). */
    dir: Entry | null
    onclose: () => void
    onqueued: (job: Job) => void
  } = $props()

  let match = $state<'self' | 'parent'>('self')
  let busy = $state(false)
  let error = $state('')

  const who = (e: Entry) => `${e.user ?? e.uid}:${e.group ?? e.gid}`
  let label = $derived(entries.length === 1 ? entries[0].name : `${entries.length} items`)

  async function submit() {
    busy = true
    error = ''
    try {
      onqueued(await api.jobs.submit({ kind: 'fixowner', paths: entries.map((e) => e.path), match }))
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="Fix owner: {label}" submitLabel="Fix owner" {busy} {error} onsubmit={submit} {onclose}>
  <p class="muted">Recursively give everything inside the same owner and group:</p>
  <label class="check">
    <input type="radio" bind:group={match} value="self" />
    <span>
      The item's own owner
      {#if entries.length === 1}<span class="mono">({who(entries[0])})</span>{:else}(each item keeps its own){/if}
      — e.g. make all of <span class="mono">/home/bob</span> owned by bob
    </span>
  </label>
  <label class="check">
    <input type="radio" bind:group={match} value="parent" />
    <span>The containing folder's owner {#if dir}<span class="mono">({who(dir)})</span>{/if}</span>
  </label>
</Modal>

<style>
  .muted { color: var(--muted); margin: 0 0 6px; }
  label.check { align-items: flex-start; }
</style>
