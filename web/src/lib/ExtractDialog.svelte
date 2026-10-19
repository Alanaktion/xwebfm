<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Account, type Entry, type Job, type OwnerPolicy } from './api'
  import Modal from './Modal.svelte'
  import OwnerPicker, { ownerFields } from './OwnerPicker.svelte'

  let {
    archives,
    dest: initialDest,
    otherDir,
    users,
    groups,
    onclose,
    onqueued,
  }: {
    archives: Entry[]
    dest: string
    otherDir: string
    users: Account[]
    groups: Account[]
    onclose: () => void
    onqueued: (job: Job) => void
  } = $props()

  let dest = $state(untrack(() => initialDest))
  let password = $state('')
  let deleteOriginals = $state(false)
  let policy = $state<OwnerPolicy>('inherit')
  let user = $state('')
  let group = $state('')
  let busy = $state(false)
  let error = $state('')

  let title = $derived(`Extract ${archives.length === 1 ? archives[0].name : `${archives.length} archives`}`)

  async function submit() {
    busy = true
    error = ''
    try {
      const job = await api.jobs.submit({
        kind: 'extract',
        items: archives.map((a) => ({ src: a.path, dst: dest })),
        password: password || undefined,
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

<Modal {title} submitLabel="Extract" {busy} {error} onsubmit={submit} {onclose}>
  {#if archives.length > 1}
    <p class="muted list">{archives.map((a) => a.name).join(', ')}</p>
  {/if}

  <label for="dest">Extract into</label>
  <div class="row">
    <input id="dest" class="mono" bind:value={dest} autocomplete="off" />
    {#if otherDir !== dest}
      <button type="button" onclick={() => (dest = otherDir)} title={otherDir}>Other pane</button>
    {/if}
  </div>
  <p class="muted hint">
    An archive holding a single top-level item extracts directly; otherwise its contents go into a new folder named
    after the archive.
  </p>

  <OwnerPicker
    bind:policy
    bind:user
    bind:group
    {users}
    {groups}
    keepLabel="As recorded in the archive (tar), else root"
    inheritLabel="Inherit from destination folder"
  />

  <label for="password">Password <span class="muted">(encrypted 7z/rar only)</span></label>
  <input id="password" type="password" bind:value={password} autocomplete="off" style="width: 100%" />

  <label class="check">
    <input type="checkbox" bind:checked={deleteOriginals} />
    Delete {archives.length === 1 ? 'the archive' : 'the archives'} (all volumes) after successful extraction
  </label>
</Modal>

<style>
  .row { display: flex; gap: 6px; }
  .row input { flex: 1; min-width: 0; }
  .list { max-height: 4.5em; overflow: auto; margin: 8px 0 0; }
  .hint { margin: 4px 0 0; font-size: 12px; }
  .muted { color: var(--muted); }
</style>
