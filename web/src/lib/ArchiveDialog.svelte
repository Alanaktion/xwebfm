<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Account, type Entry, type Job, type OwnerPolicy } from './api'
  import { joinPath } from './format'
  import Modal from './Modal.svelte'
  import OwnerPicker, { ownerFields } from './OwnerPicker.svelte'

  let {
    sources,
    dir,
    formats,
    users,
    groups,
    onclose,
    onqueued,
  }: {
    sources: Entry[]
    dir: string
    formats: string[]
    users: Account[]
    groups: Account[]
    onclose: () => void
    onqueued: (job: Job) => void
  } = $props()

  const defaultName = untrack(() =>
    sources.length === 1 ? sources[0].name : dir === '/' ? 'archive' : dir.slice(dir.lastIndexOf('/') + 1),
  )
  let name = $state(defaultName)
  // tar.gz keeps Unix owners and permissions; fall back to whatever exists.
  let format = $state(untrack(() => (formats.includes('tar.gz') ? 'tar.gz' : (formats[0] ?? 'zip'))))
  let dest = $state(untrack(() => dir))
  let overwrite = $state(false)
  let deleteOriginals = $state(false)
  let policy = $state<OwnerPolicy>('inherit')
  let user = $state('')
  let group = $state('')
  let busy = $state(false)
  let error = $state('')

  let fileName = $derived(`${name.trim()}.${format}`)
  let title = $derived(`Archive ${sources.length === 1 ? sources[0].name : `${sources.length} items`}`)

  async function submit() {
    if (!name.trim()) {
      error = 'Enter a name'
      return
    }
    busy = true
    error = ''
    try {
      const job = await api.jobs.submit({
        kind: 'archive',
        paths: sources.map((s) => s.path),
        dst: joinPath(dest, fileName),
        format,
        overwrite,
        deleteOriginals,
        ...ownerFields(policy, user, group),
      })
      onqueued(job)
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal {title} submitLabel="Create archive" {busy} {error} onsubmit={submit} {onclose}>
  {#if sources.length > 1}
    <p class="muted list">{sources.map((s) => s.name).join(', ')}</p>
  {/if}

  <label for="name">Name</label>
  <div class="row">
    <!-- svelte-ignore a11y_autofocus -->
    <input id="name" bind:value={name} autocomplete="off" autofocus />
    <select bind:value={format} aria-label="Format">
      {#each formats as f}<option value={f}>.{f}</option>{/each}
    </select>
  </div>

  <label for="dest">In folder</label>
  <input id="dest" class="mono" bind:value={dest} autocomplete="off" style="width: 100%" />

  <OwnerPicker
    bind:policy
    bind:user
    bind:group
    {users}
    {groups}
    keepLabel="Owned by root"
    inheritLabel="Inherit from destination folder"
  />

  <label class="check"><input type="checkbox" bind:checked={overwrite} /> Replace an existing {fileName}</label>
  <label class="check">
    <input type="checkbox" bind:checked={deleteOriginals} />
    Delete the originals after the archive is created
  </label>
  {#if format === '7z'}
    <p class="muted hint">7z archives are written by the 7-Zip program, which stores symlinks as links.</p>
  {/if}
</Modal>

<style>
  .row { display: flex; gap: 6px; }
  .row input { flex: 1; min-width: 0; }
  .list { max-height: 4.5em; overflow: auto; margin: 8px 0 0; }
  .hint { margin: 8px 0 0; font-size: 12px; }
  .muted { color: var(--muted); }
</style>
