<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Account, type Entry } from './api'
  import Modal from './Modal.svelte'

  let {
    entry,
    users,
    groups,
    onclose,
    ondone,
  }: {
    entry: Entry
    users: Account[]
    groups: Account[]
    onclose: () => void
    ondone: (msg: string) => void
  } = $props()

  // Accept either a name or a numeric ID, like chown(1).
  let owner = $state(untrack(() => entry.user ?? String(entry.uid)))
  let group = $state(untrack(() => entry.group ?? String(entry.gid)))
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
    try {
      const e = await api.chown({ path: entry.path, uid, gid, recursive })
      ondone(`Owner of ${entry.name} set to ${e.user ?? e.uid}:${e.group ?? e.gid}`)
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="Change owner: {entry.name}" {busy} {error} onsubmit={submit} {onclose}>
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

  {#if entry.type === 'symlink'}
    <p class="note">The symlink itself is changed, not its target.</p>
  {/if}
  {#if entry.type === 'dir'}
    <label class="check"><input type="checkbox" bind:checked={recursive} /> Apply recursively (symlinks are not followed)</label>
  {/if}
</Modal>

<style>
  .note { color: var(--muted); }
</style>
