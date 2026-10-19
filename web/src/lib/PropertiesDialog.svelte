<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type Account, type ACLEntry, type Flags, type Job, type Props } from './api'
  import { humanSize, modeString, octal } from './format'
  import Modal from './Modal.svelte'

  let {
    path,
    users,
    groups,
    onclose,
    onowner,
    onmode,
    onqueued,
    onchanged,
  }: {
    path: string
    users: Account[]
    groups: Account[]
    onclose: () => void
    onowner: () => void
    onmode: () => void
    onqueued: (job: Job) => void
    onchanged: () => void
  } = $props()

  let info = $state<Props | null>(null)
  let error = $state('')
  let aclError = $state('')
  let busy = $state(false)
  let named = $state<ACLEntry[]>([])
  let defaults = $state<ACLEntry[]>([])
  let hasDefault = $state(false)
  let recursive = $state(false)
  let dirty = $state(false)
  let newWho = $state('')
  let newDefWho = $state('')

  let e = $derived(info?.entry)
  let isDir = $derived(e?.type === 'dir')
  const perms = [
    ['r', 4],
    ['w', 2],
    ['x', 1],
  ] as const
  const flagList: [keyof Flags, string, string][] = [
    ['immutable', 'Immutable', "can't be changed, renamed, deleted or linked (even by root) until cleared"],
    ['appendOnly', 'Append only', 'can only be appended to'],
    ['noCow', 'No copy-on-write', 'btrfs: disables CoW (affects new data only)'],
    ['noDump', 'No dump', 'skipped by dump backups'],
    ['noAtime', 'No atime', "access time isn't updated"],
  ]

  onMount(load)

  async function load() {
    try {
      info = await api.props(path)
      named = info.acl?.named.map((x) => ({ ...x })) ?? []
      defaults = info.acl?.default.map((x) => ({ ...x })) ?? []
      hasDefault = info.acl?.hasDefault ?? false
      dirty = false
    } catch (err) {
      error = (err as Error).message
    }
  }

  /** Resolves "alice", "@staff" / "g:staff", or numeric IDs to an entry. */
  function parseWho(input: string): ACLEntry {
    let s = input.trim()
    let kind: 'user' | 'group' = 'user'
    if (s.startsWith('@') || s.startsWith('g:')) {
      kind = 'group'
      s = s.replace(/^(@|g:)/, '')
    } else if (s.startsWith('u:')) {
      s = s.slice(2)
    }
    const list = kind === 'user' ? users : groups
    const acct = /^\d+$/.test(s) ? list.find((a) => a.id === Number(s)) : list.find((a) => a.name === s)
    if (!acct && !/^\d+$/.test(s)) throw new Error(`Unknown ${kind} "${s}"`)
    return { kind, id: acct?.id ?? Number(s), name: acct?.name, perm: 5 }
  }

  function add(list: 'named' | 'default') {
    aclError = ''
    try {
      const entry = parseWho(list === 'named' ? newWho : newDefWho)
      if (list === 'named') {
        named = [...named.filter((x) => !(x.kind === entry.kind && x.id === entry.id)), entry]
        newWho = ''
      } else {
        defaults = [...defaults.filter((x) => !(x.kind === entry.kind && x.id === entry.id)), entry]
        newDefWho = ''
        hasDefault = true
      }
      dirty = true
    } catch (err) {
      aclError = (err as Error).message
    }
  }

  async function saveACL() {
    busy = true
    aclError = ''
    const acl = { named, default: defaults, hasDefault }
    try {
      if (recursive && isDir) {
        onqueued(await api.jobs.submit({ kind: 'acl', paths: [path], acl, recursive: true }))
        return
      }
      info = await api.setAcl(path, acl)
      dirty = false
      onchanged()
    } catch (err) {
      aclError = (err as Error).message
    } finally {
      busy = false
    }
  }

  async function toggleFlag(key: keyof Flags) {
    if (!info?.flags) return
    error = ''
    try {
      info = await api.setFlags(path, { ...info.flags, [key]: !info.flags[key] })
      onchanged()
    } catch (err) {
      error = (err as Error).message
    }
  }

  const who = (x: ACLEntry) => `${x.kind === 'group' ? 'group ' : ''}${x.name ?? x.id}`
</script>

{#snippet aclRows(list: ACLEntry[], update: (l: ACLEntry[]) => void)}
  {#each list as x, i}
    <tr>
      <td class:orphan={!x.name}>{who(x)}{#if !x.name}<span title="no such {x.kind}"> ⚠</span>{/if}</td>
      {#each perms as [label, bit]}
        <td>
          <label class="check"
            ><input
              type="checkbox"
              checked={(x.perm & bit) !== 0}
              onchange={() => {
                list[i] = { ...x, perm: x.perm ^ bit }
                update([...list])
                dirty = true
              }}
            />{label}</label
          >
        </td>
      {/each}
      <td>
        <button
          type="button"
          class="small"
          onclick={() => {
            update(list.filter((_, j) => j !== i))
            dirty = true
          }}>Remove</button
        >
      </td>
    </tr>
  {/each}
{/snippet}

<Modal title="Properties: {e?.name ?? path}" wide {error} {onclose}>
  {#if !info}
    <p class="muted">Loading…</p>
  {:else if e}
    <dl class="info">
      <dt>Path</dt>
      <dd class="mono">{e.path}{#if e.linkTarget} → {e.linkTarget}{/if}</dd>
      <dt>Type</dt>
      <dd>{e.type}</dd>
      <dt>Size</dt>
      <dd>{humanSize(e.size)} · {humanSize(e.disk)} on disk</dd>
      <dt>Owner</dt>
      <dd>
        <span class="mono" class:orphan={!e.user || !e.group}>{e.user ?? e.uid}:{e.group ?? e.gid}</span>
        <button type="button" class="small" onclick={onowner}>Change…</button>
      </dd>
      <dt>Mode</dt>
      <dd>
        <span class="mono">{modeString(e.type, e.mode)}{e.acl ? '+' : ''} ({octal(e.mode)})</span>
        {#if e.type !== 'symlink'}<button type="button" class="small" onclick={onmode}>Change…</button>{/if}
      </dd>
      <dt>Modified</dt>
      <dd>{new Date(e.mtime).toLocaleString()}</dd>
      <dt>Inode</dt>
      <dd>{e.inode} · {e.nlink} link{e.nlink === 1 ? '' : 's'}</dd>
    </dl>

    {#if info.acl}
      <h3>Access control list</h3>
      <p class="muted small-text">
        Owner, group and other permissions come from the mode. Named entries grant extra users or groups access
        (limited by the mask{info.acl.mask !== undefined ? `, now ${modeString('file', info.acl.mask).slice(7)}` : ''}).
      </p>
      <table class="acl">
        <tbody>
          {@render aclRows(named, (l) => (named = l))}
          {#if !named.length}<tr><td class="muted" colspan="5">No named entries</td></tr>{/if}
        </tbody>
      </table>
      <div class="add">
        <input list="info-accounts" bind:value={newWho} placeholder="user, or @group" autocomplete="off" />
        <button type="button" onclick={() => add('named')} disabled={!newWho.trim()}>Add</button>
      </div>

      {#if isDir}
        <h4>
          <label class="check"><input type="checkbox" bind:checked={hasDefault} onchange={() => (dirty = true)} /> Default ACL for new files and folders</label>
        </h4>
        {#if hasDefault}
          <table class="acl">
            <tbody>
              {@render aclRows(defaults, (l) => (defaults = l))}
              {#if !defaults.length}<tr><td class="muted" colspan="5">Only owner/group/other from the folder's mode</td></tr>{/if}
            </tbody>
          </table>
          <div class="add">
            <input list="info-accounts" bind:value={newDefWho} placeholder="user, or @group" autocomplete="off" />
            <button type="button" onclick={() => add('default')} disabled={!newDefWho.trim()}>Add</button>
          </div>
        {/if}
        <label class="check"><input type="checkbox" bind:checked={recursive} /> Apply named entries to everything inside (files only get execute if already executable)</label>
      {/if}
      {#if aclError}<p class="error">{aclError}</p>{/if}
      <div class="row-end">
        <button type="button" onclick={load} disabled={!dirty || busy}>Revert</button>
        <button type="button" class="primary" onclick={saveACL} disabled={(!dirty && !recursive) || busy}>Save ACL</button>
      </div>
      <datalist id="info-accounts">
        {#each users as u}<option value={u.name}>user {u.id}</option>{/each}
        {#each groups as g}<option value={'@' + g.name}>group {g.id}</option>{/each}
      </datalist>
    {/if}

    {#if info.flags}
      <h3>Attributes</h3>
      <ul class="flags">
        {#each flagList as [key, label, help]}
          <li>
            <label class="check">
              <input type="checkbox" checked={info.flags[key]} onchange={() => toggleFlag(key)} />
              {label} <span class="muted small-text">— {help}</span>
            </label>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</Modal>

<style>
  .info { display: grid; grid-template-columns: auto 1fr; gap: 4px 14px; margin: 0; }
  dt { color: var(--muted); }
  dd { margin: 0; word-break: break-all; }
  h3 { font-size: 14px; margin: 18px 0 4px; }
  h4 { margin: 12px 0 4px; font-weight: normal; }
  .muted { color: var(--muted); }
  .small-text { font-size: 12px; }
  .orphan { color: var(--danger); }
  .acl { border-collapse: collapse; margin: 6px 0; }
  .acl td { padding: 2px 10px 2px 0; }
  .acl label { margin: 0; }
  .add { display: flex; gap: 6px; margin: 4px 0 8px; }
  .add input { flex: 1; max-width: 260px; }
  button.small { padding: 0 8px; font-size: 12px; margin-left: 6px; }
  .row-end { display: flex; justify-content: flex-end; gap: 8px; margin-top: 6px; }
  .flags { list-style: none; padding: 0; margin: 0; }
  .flags label { margin: 3px 0; }
</style>
