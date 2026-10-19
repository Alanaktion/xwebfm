<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Account, type Entry } from './api'
  import Modal from './Modal.svelte'

  let {
    entries,
    users,
    groups,
    onclose,
    ondone,
  }: {
    entries: Entry[]
    users: Account[]
    groups: Account[]
    onclose: () => void
    ondone: (msg: string, error?: boolean) => void
  } = $props()

  let label = $derived(entries.length === 1 ? entries[0].name : `${entries.length} items`)

  // Accept either a name or a numeric ID, like chown(1).
  let owner = $state(untrack(() => entries[0].user ?? String(entries[0].uid)))
  let group = $state(untrack(() => entries[0].group ?? String(entries[0].gid)))
  let recursive = $state(false)
  let busy = $state(false)
  let error = $state('')

  function resolve(input: string, list: Account[], kind: string): number {
    const s = input.trim()
    if (/^\d+$/.test(s)) return Number(s)
    const acct = list.find((a) => a.name === s)
    if (!acct) throw new Error(`Unknown ${kind} "${s}"`)
    return acct.id
  }

  async function submit() {
    error = ''
    let uid: number, gid: number
    try {
      uid = resolve(owner, users, 'user')
      gid = resolve(group, groups, 'group')
    } catch (e) {
      error = (e as Error).message
      return
    }
    busy = true
    const failures: string[] = []
    for (const e of entries) {
      try {
        await api.chown({ path: e.path, uid, gid, recursive: recursive && e.type === 'dir' })
      } catch (err) {
        failures.push(`${e.name}: ${(err as Error).message}`)
      }
    }
    busy = false
    if (failures.length) {
      error = failures.join('\n')
      if (failures.length < entries.length) ondone(`Changed ${entries.length - failures.length} of ${entries.length}`, true)
      return
    }
    ondone(`Owner of ${label} set to ${owner.trim()}:${group.trim()}`)
  }
</script>

<Modal title="Change owner: {label}" {busy} {error} onsubmit={submit} {onclose}>
  <label for="owner">Owner (name or UID)</label>
  <input id="owner" list="users" bind:value={owner} autocomplete="off" />
  <datalist id="users">
    {#each users as u}<option value={u.name}>{u.id}</option>{/each}
  </datalist>

  <label for="group">Group (name or GID)</label>
  <input id="group" list="groups" bind:value={group} autocomplete="off" />
  <datalist id="groups">
    {#each groups as g}<option value={g.name}>{g.id}</option>{/each}
  </datalist>

  {#if entries.some((e) => e.type === 'symlink')}
    <p class="note">Symlinks themselves are changed, not their targets.</p>
  {/if}
  {#if entries.some((e) => e.type === 'dir')}
    <label class="check"><input type="checkbox" bind:checked={recursive} /> Apply recursively (symlinks are not followed)</label>
  {/if}
</Modal>

<style>
  .note { color: var(--muted); }
</style>
