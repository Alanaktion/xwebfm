<script lang="ts">
  import { api, type Entry, type Job } from './api'
  import Modal from './Modal.svelte'

  let { entries, onclose, onqueued }: { entries: Entry[]; onclose: () => void; onqueued: (job: Job) => void } =
    $props()

  let busy = $state(false)
  let error = $state('')
  let dirs = $derived(entries.filter((e) => e.type === 'dir').length)

  async function submit() {
    busy = true
    error = ''
    try {
      onqueued(await api.jobs.submit({ kind: 'delete', paths: entries.map((e) => e.path) }))
    } catch (err) {
      error = (err as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal
  title="Delete {entries.length === 1 ? entries[0].name : `${entries.length} items`}?"
  submitLabel="Delete"
  danger
  {busy}
  {error}
  onsubmit={submit}
  {onclose}
>
  <ul class="mono">
    {#each entries.slice(0, 10) as e}<li>{e.path}{e.type === 'dir' ? '/' : ''}</li>{/each}
    {#if entries.length > 10}<li>…and {entries.length - 10} more</li>{/if}
  </ul>
  {#if dirs}<p class="error">Folders are deleted recursively, with everything inside them.</p>{/if}
  <p>This can't be undone.</p>
</Modal>

<style>
  ul { max-height: 12em; overflow: auto; padding-left: 20px; }
</style>
