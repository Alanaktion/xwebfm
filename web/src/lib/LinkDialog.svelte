<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Entry } from './api'
  import { joinPath } from './format'
  import Modal from './Modal.svelte'

  let {
    entries,
    dest: initialDest,
    onclose,
    ondone,
  }: {
    entries: Entry[]
    dest: string
    onclose: () => void
    ondone: (msg: string, error?: boolean) => void
  } = $props()

  let dest = $state(untrack(() => initialDest))
  let kind = $state<'symlink' | 'hard'>('symlink')
  let relative = $state(true)
  let busy = $state(false)
  let error = $state('')
  let hasDirs = $derived(entries.some((e) => e.type === 'dir'))

  async function submit() {
    busy = true
    error = ''
    const failures: string[] = []
    for (const e of entries) {
      try {
        await api.link({ target: e.path, path: joinPath(dest, e.name), kind, relative })
      } catch (err) {
        failures.push(`${e.name}: ${(err as Error).message}`)
      }
    }
    busy = false
    if (failures.length) {
      error = failures.join('\n')
      return
    }
    ondone(`Created ${entries.length} ${kind === 'hard' ? 'hard link' : 'symlink'}${entries.length === 1 ? '' : 's'} in ${dest}`)
  }
</script>

<Modal title="Link {entries.length === 1 ? entries[0].name : `${entries.length} items`}" submitLabel="Create" {busy} {error} onsubmit={submit} {onclose}>
  <label for="link-dest">Create links in</label>
  <input id="link-dest" class="mono" bind:value={dest} style="width: 100%" autocomplete="off" />
  <label class="check"><input type="radio" bind:group={kind} value="symlink" /> Symbolic link</label>
  {#if kind === 'symlink'}
    <label class="check sub"><input type="checkbox" bind:checked={relative} /> Relative target (keeps working if both move together)</label>
  {/if}
  <label class="check">
    <input type="radio" bind:group={kind} value="hard" disabled={hasDirs} /> Hard link
    {#if hasDirs}<span class="muted">(not possible for folders)</span>{/if}
  </label>
  {#if kind === 'hard'}
    <p class="muted">A hard link is the same file under a second name: same contents, owner and permissions; it must be on the same filesystem.</p>
  {/if}
</Modal>

<style>
  .sub { margin-left: 24px; }
  .muted { color: var(--muted); font-size: 12px; }
</style>
