<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Entry } from './api'
  import Modal from './Modal.svelte'

  let { entry, onclose, ondone }: { entry: Entry; onclose: () => void; ondone: (msg: string) => void } = $props()

  let name = $state(untrack(() => entry.name))
  let busy = $state(false)
  let error = $state('')

  async function submit() {
    if (name === entry.name) return onclose()
    busy = true
    error = ''
    try {
      await api.rename(entry.path, name)
      ondone(`Renamed ${entry.name} to ${name}`)
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="Rename {entry.name}" submitLabel="Rename" {busy} {error} onsubmit={submit} {onclose}>
  <label for="name">New name</label>
  <!-- svelte-ignore a11y_autofocus -->
  <input id="name" bind:value={name} style="width: 100%" autocomplete="off" autofocus />
</Modal>
