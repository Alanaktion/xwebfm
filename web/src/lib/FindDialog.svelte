<script lang="ts">
  import { api, type Account, type FileType, type FindQuery, type Job } from './api'
  import Modal from './Modal.svelte'

  let {
    root,
    rootOwner,
    users,
    groups,
    onclose,
    onstart,
  }: {
    root: string
    rootOwner: string
    users: Account[]
    groups: Account[]
    onclose: () => void
    /** The search job started; label describes it for the results header. */
    onstart: (job: Job, label: string) => void
  } = $props()

  const units = { KiB: 1024, MiB: 1024 ** 2, GiB: 1024 ** 3 }
  type Unit = keyof typeof units

  let name = $state('')
  let type = $state<FileType | ''>('')
  let owner = $state('')
  let group = $state('')
  let minSize = $state('')
  let minUnit = $state<Unit>('MiB')
  let maxSize = $state('')
  let maxUnit = $state<Unit>('MiB')
  let newerDays = $state('')
  let olderDays = $state('')
  let orphaned = $state(false)
  let foreignOwner = $state(false)
  let worldWritable = $state(false)
  let setuid = $state(false)
  let crossMounts = $state(false)
  let busy = $state(false)
  let error = $state('')

  function id(input: string, list: Account[], kind: string): number | undefined {
    const s = input.trim()
    if (!s) return undefined
    if (/^\d+$/.test(s)) return Number(s)
    const a = list.find((x) => x.name === s)
    if (!a) throw new Error(`Unknown ${kind} "${s}"`)
    return a.id
  }

  function num(s: string, what: string): number | undefined {
    if (!s.trim()) return undefined
    const n = Number(s)
    if (!isFinite(n) || n < 0) throw new Error(`Invalid ${what}`)
    return n
  }

  async function submit() {
    error = ''
    let query: FindQuery
    try {
      const min = num(minSize, 'minimum size')
      const max = num(maxSize, 'maximum size')
      const newer = num(newerDays, 'age')
      const older = num(olderDays, 'age')
      const day = 86400_000
      query = {
        name: name.trim() || undefined,
        type: type || undefined,
        uid: id(owner, users, 'user'),
        gid: id(group, groups, 'group'),
        minSize: min === undefined ? undefined : Math.round(min * units[minUnit]),
        maxSize: max === undefined ? undefined : Math.round(max * units[maxUnit]),
        modifiedAfter: newer === undefined ? undefined : new Date(Date.now() - newer * day).toISOString(),
        modifiedBefore: older === undefined ? undefined : new Date(Date.now() - older * day).toISOString(),
        orphaned: orphaned || undefined,
        foreignOwner: foreignOwner || undefined,
        worldWritable: worldWritable || undefined,
        setuid: setuid || undefined,
        crossMounts: crossMounts || undefined,
      }
    } catch (e) {
      error = (e as Error).message
      return
    }
    const parts = [
      query.name && `“${query.name}”`,
      orphaned && 'orphaned',
      foreignOwner && `not owned by ${rootOwner}`,
      worldWritable && 'world-writable',
      setuid && 'setuid/setgid',
    ].filter(Boolean)
    busy = true
    try {
      const job = await api.jobs.submit({ kind: 'find', paths: [root], query })
      onstart(job, parts.join(', ') || 'Search')
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="Find in {root}" submitLabel="Search" {busy} {error} onsubmit={submit} {onclose}>
  <label for="find-name">Name <span class="muted">(* and ? wildcards; plain text matches anywhere)</span></label>
  <!-- svelte-ignore a11y_autofocus -->
  <input id="find-name" bind:value={name} autocomplete="off" style="width: 100%" autofocus />

  <div class="grid">
    <label for="find-type">Type</label>
    <select id="find-type" bind:value={type}>
      <option value="">Any</option>
      <option value="file">File</option>
      <option value="dir">Folder</option>
      <option value="symlink">Symlink</option>
    </select>

    <label for="find-owner">Owner</label>
    <input id="find-owner" list="find-users" bind:value={owner} placeholder="user or UID" autocomplete="off" />
    <label for="find-group">Group</label>
    <input id="find-group" list="find-groups" bind:value={group} placeholder="group or GID" autocomplete="off" />

    <label for="find-min">Size at least</label>
    <span class="pair">
      <input id="find-min" bind:value={minSize} inputmode="decimal" size="6" />
      <select bind:value={minUnit} aria-label="Unit">{#each Object.keys(units) as u}<option>{u}</option>{/each}</select>
    </span>
    <label for="find-max">Size at most</label>
    <span class="pair">
      <input id="find-max" bind:value={maxSize} inputmode="decimal" size="6" />
      <select bind:value={maxUnit} aria-label="Unit">{#each Object.keys(units) as u}<option>{u}</option>{/each}</select>
    </span>

    <label for="find-newer">Modified within</label>
    <span class="pair"><input id="find-newer" bind:value={newerDays} inputmode="numeric" size="6" /> days</span>
    <label for="find-older">Not modified for</label>
    <span class="pair"><input id="find-older" bind:value={olderDays} inputmode="numeric" size="6" /> days</span>
  </div>
  <datalist id="find-users">{#each users as u}<option value={u.name}>{u.id}</option>{/each}</datalist>
  <datalist id="find-groups">{#each groups as g}<option value={g.name}>{g.id}</option>{/each}</datalist>

  <fieldset>
    <legend>Problems to look for</legend>
    <label class="check"><input type="checkbox" bind:checked={foreignOwner} /> Not owned by {rootOwner} (owner of this folder)</label>
    <label class="check"><input type="checkbox" bind:checked={orphaned} /> Owner or group no longer exists</label>
    <label class="check"><input type="checkbox" bind:checked={worldWritable} /> World-writable</label>
    <label class="check"><input type="checkbox" bind:checked={setuid} /> Setuid / setgid</label>
  </fieldset>
  <label class="check"><input type="checkbox" bind:checked={crossMounts} /> Include other filesystems mounted below</label>
</Modal>

<style>
  .grid { display: grid; grid-template-columns: auto 1fr; gap: 6px 10px; align-items: center; margin-top: 10px; }
  .grid label { margin: 0; }
  .pair { display: flex; gap: 6px; align-items: center; }
  fieldset { border: 1px solid var(--border); border-radius: 6px; margin: 12px 0 4px; padding: 6px 10px; }
  legend { color: var(--muted); font-size: 12px; }
  fieldset label { margin: 4px 0; }
  .muted { color: var(--muted); font-weight: normal; }
</style>
