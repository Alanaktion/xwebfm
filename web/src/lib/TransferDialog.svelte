<script lang="ts">
  import { untrack } from 'svelte'
  import { api, ApiError, type Account, type Entry, type OwnerPolicy, type ReflinkMode } from './api'
  import { humanSize, joinPath } from './format'
  import Modal from './Modal.svelte'
  import OwnerPicker, { ownerFields } from './OwnerPicker.svelte'

  let {
    op: initialOp,
    sources,
    dest: initialDest,
    users,
    groups,
    onclose,
    ondone,
  }: {
    op: 'copy' | 'move'
    sources: Entry[]
    dest: string
    users: Account[]
    groups: Account[]
    onclose: () => void
    ondone: (msg: string, error?: boolean) => void
  } = $props()

  let op = $state(untrack(() => initialOp))
  let dest = $state(untrack(() => initialDest))
  let reflink = $state<ReflinkMode>('auto')
  let policy = $state<OwnerPolicy>('keep')
  let user = $state('')
  let group = $state('')
  let overwrite = $state(false)
  let busy = $state(false)
  let error = $state('')
  let progress = $state('')
  let destInfo = $state<Entry | null>(null)
  let policyTouched = false

  // Look up the destination folder to show its owner and, when it belongs to
  // someone else, default to handing the files over to them.
  $effect(() => {
    const d = dest
    const timer = setTimeout(async () => {
      destInfo = await api.stat(d).catch(() => null)
      if (!policyTouched && destInfo?.type === 'dir') {
        const foreign = sources.some((s) => s.uid !== destInfo!.uid || s.gid !== destInfo!.gid)
        policy = foreign ? 'inherit' : 'keep'
      }
    }, 150)
    return () => clearTimeout(timer)
  })

  let title = $derived(
    `${op === 'copy' ? 'Copy' : 'Move'} ${sources.length === 1 ? sources[0].name : `${sources.length} items`}`,
  )

  async function submit() {
    busy = true
    error = ''
    // Like mc: an existing directory receives the sources; otherwise a
    // single source is copied/moved to that exact path.
    let target: (s: Entry) => string
    const info = await api.stat(dest).catch(() => null)
    if (info?.type === 'dir') {
      target = (s) => joinPath(info.path, s.name)
    } else if (sources.length === 1 && (!info || overwrite)) {
      target = () => dest
    } else {
      error = info ? `${dest} exists and is not a directory` : `Directory ${dest} does not exist`
      busy = false
      return
    }

    const owner = ownerFields(policy, user, group)
    const failures: string[] = []
    let files = 0,
      reflinked = 0,
      bytes = 0,
      copied = false
    for (const [i, s] of sources.entries()) {
      progress = `${op === 'copy' ? 'Copying' : 'Moving'} ${i + 1}/${sources.length}: ${s.name}`
      try {
        if (op === 'copy') {
          const r = await api.copy({ src: s.path, dst: target(s), reflink, overwrite, ...owner })
          files += r.files
          reflinked += r.reflinked
          bytes += r.bytes
        } else {
          const r = await api.move({ src: s.path, dst: target(s), overwrite, ...owner })
          copied ||= r.copied
        }
      } catch (e) {
        failures.push(`${s.name}: ${(e as ApiError).message}`)
      }
    }
    busy = false
    progress = ''
    if (failures.length) {
      error = failures.join('\n')
      if (failures.length < sources.length) ondone(`${sources.length - failures.length} of ${sources.length} done`, true)
      return
    }
    const what = sources.length === 1 ? sources[0].name : `${sources.length} items`
    ondone(
      op === 'copy'
        ? `Copied ${what}: ${files} files, ${humanSize(bytes)}` +
            (reflink === 'never' ? '' : `, ${reflinked} reflinked`)
        : `Moved ${what}${copied ? ' (across filesystems, copied and removed)' : ''}`,
    )
  }
</script>

<Modal {title} submitLabel={op === 'copy' ? 'Copy' : 'Move'} {busy} {error} onsubmit={submit} {onclose}>
  <div class="ops">
    <label class="check"><input type="radio" bind:group={op} value="copy" /> Copy</label>
    <label class="check"><input type="radio" bind:group={op} value="move" /> Move</label>
  </div>

  {#if sources.length > 1}
    <p class="muted list">{sources.map((s) => s.name).join(', ')}</p>
  {/if}

  <label for="dest">To</label>
  <input id="dest" class="mono" bind:value={dest} style="width: 100%" autocomplete="off" />
  {#if destInfo}
    <p class="muted hint">
      {destInfo.type === 'dir' ? 'Folder' : 'Exists'}, owned by {destInfo.user ?? destInfo.uid}:{destInfo.group ??
        destInfo.gid}
    </p>
  {/if}

  <div onchange={() => (policyTouched = true)} role="group">
    <OwnerPicker
      bind:policy
      bind:user
      bind:group
      {users}
      {groups}
      keepLabel={op === 'move' ? 'Leave owner unchanged' : 'Keep source owner'}
    />
  </div>

  {#if op === 'copy'}
    <label for="reflink">Reflink</label>
    <select id="reflink" bind:value={reflink}>
      <option value="auto">Auto — reflink if possible, otherwise copy</option>
      <option value="always">Always — fail if reflink unsupported</option>
      <option value="never">Never — full data copy</option>
    </select>
  {/if}

  <label class="check">
    <input type="checkbox" bind:checked={overwrite} />
    {op === 'copy' ? 'Overwrite existing files (merges directories)' : 'Replace existing destination'}
  </label>
  {#if progress}<p class="muted">{progress}</p>{/if}
</Modal>

<style>
  .ops { display: flex; gap: 16px; }
  .list { max-height: 4.5em; overflow: auto; margin: 8px 0 0; }
  .hint { margin: 4px 0 0; font-size: 12px; }
  .muted { color: var(--muted); }
</style>
