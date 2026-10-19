<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Entry, type ReflinkMode } from './api'
  import Modal from './Modal.svelte'

  let { entry, onclose, ondone }: { entry: Entry; onclose: () => void; ondone: (msg: string) => void } = $props()

  let dst = $state(untrack(() => entry.path + '.copy'))
  let reflink = $state<ReflinkMode>('auto')
  let preserve = $state(true)
  let overwrite = $state(false)
  let busy = $state(false)
  let error = $state('')

  async function submit() {
    busy = true
    error = ''
    try {
      const res = await api.copy({ src: entry.path, dst, reflink, preserve, overwrite })
      ondone(`Copied to ${res.entry.path} (${res.reflinked ? 'reflinked, data shared' : 'data copied'})`)
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="Copy: {entry.name}" submitLabel="Copy" {busy} {error} onsubmit={submit} {onclose}>
  <label for="dst">Destination path</label>
  <input id="dst" class="mono" bind:value={dst} style="width: 100%" autocomplete="off" />

  <label for="reflink">Reflink</label>
  <select id="reflink" bind:value={reflink}>
    <option value="auto">Auto — reflink if possible, otherwise copy</option>
    <option value="always">Always — fail if reflink unsupported</option>
    <option value="never">Never — full data copy</option>
  </select>

  <label class="check"><input type="checkbox" bind:checked={preserve} /> Preserve owner, mode and timestamps</label>
  <label class="check"><input type="checkbox" bind:checked={overwrite} /> Overwrite if destination exists</label>
</Modal>
