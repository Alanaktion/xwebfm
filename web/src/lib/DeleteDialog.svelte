<script lang="ts">
  import { api, type Entry } from './api'
  import Modal from './Modal.svelte'

  let {
    entries,
    onclose,
    ondone,
  }: { entries: Entry[]; onclose: () => void; ondone: (msg: string, error?: boolean) => void } = $props()

  let busy = $state(false)
  let error = $state('')
  let dirs = $derived(entries.filter((e) => e.type === 'dir').length)

  async function submit() {
    busy = true
    error = ''
    const failures: string[] = []
    for (const e of entries) {
      try {
        await api.remove(e.path)
      } catch (err) {
        failures.push(`${e.name}: ${(err as Error).message}`)
      }
    }
    busy = false
    if (failures.length) {
      error = failures.join('\n')
      if (failures.length < entries.length) ondone(`Deleted ${entries.length - failures.length} of ${entries.length}`, true)
      return
    }
    ondone(`Deleted ${entries.length === 1 ? entries[0].name : `${entries.length} items`}`)
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
