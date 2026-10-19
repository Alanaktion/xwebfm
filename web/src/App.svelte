<script lang="ts">
  import { api, type Account, type Entry } from './lib/api'
  import { parentPath } from './lib/format'
  import FileTable from './lib/FileTable.svelte'
  import ChownDialog from './lib/ChownDialog.svelte'
  import ChmodDialog from './lib/ChmodDialog.svelte'
  import CopyDialog from './lib/CopyDialog.svelte'

  // The current directory lives in the URL hash so reloads and back/forward work.
  const hashPath = () => decodeURIComponent(location.hash.slice(1)) || '/'

  let path = $state(hashPath())
  let entries = $state<Entry[]>([])
  let loading = $state(false)
  let error = $state('')
  let notice = $state('')
  let users = $state<Account[]>([])
  let groups = $state<Account[]>([])
  let dialog = $state<{ action: 'chown' | 'chmod' | 'copy'; entry: Entry } | null>(null)

  let crumbs = $derived(
    path
      .split('/')
      .filter(Boolean)
      .map((name, i, parts) => ({ name, path: '/' + parts.slice(0, i + 1).join('/') })),
  )

  async function load() {
    loading = true
    error = ''
    try {
      const res = await api.list(path)
      entries = res.entries
      if (res.path !== path) navigate(res.path)
    } catch (e) {
      error = (e as Error).message
      entries = []
    } finally {
      loading = false
    }
  }

  function navigate(p: string) {
    location.hash = encodeURI(p)
  }

  $effect(() => {
    const onHash = () => (path = hashPath())
    window.addEventListener('hashchange', onHash)
    api.users().then((u) => (users = u)).catch(() => {})
    api.groups().then((g) => (groups = g)).catch(() => {})
    return () => window.removeEventListener('hashchange', onHash)
  })

  $effect(() => {
    path // reload whenever the path changes
    notice = ''
    load()
  })

  function done(msg: string) {
    dialog = null
    notice = msg
    load()
  }
</script>

<header>
  <strong>xwebfm</strong>
  <nav class="crumbs mono">
    <button class="link" onclick={() => navigate('/')}>/</button>
    {#each crumbs as c, i}
      {#if i > 0}<span>/</span>{/if}
      <button class="link" onclick={() => navigate(c.path)}>{c.name}</button>
    {/each}
  </nav>
  <span class="spacer"></span>
  <button onclick={() => navigate(parentPath(path))} disabled={path === '/'}>Up</button>
  <button onclick={load} disabled={loading}>Refresh</button>
</header>

<main>
  {#if notice}<p class="notice">{notice}</p>{/if}
  {#if error}
    <p class="error">{error}</p>
  {:else}
    <FileTable {entries} onopen={(e) => navigate(e.path)} onaction={(action, entry) => (dialog = { action, entry })} />
  {/if}
</main>

{#if dialog?.action === 'chown'}
  <ChownDialog entry={dialog.entry} {users} {groups} onclose={() => (dialog = null)} ondone={done} />
{:else if dialog?.action === 'chmod'}
  <ChmodDialog entry={dialog.entry} onclose={() => (dialog = null)} ondone={done} />
{:else if dialog?.action === 'copy'}
  <CopyDialog entry={dialog.entry} onclose={() => (dialog = null)} ondone={done} />
{/if}

<style>
  header {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--bg);
  }
  .crumbs { display: flex; flex-wrap: wrap; gap: 2px; align-items: center; color: var(--muted); }
  .spacer { flex: 1; }
  main { padding: 8px 16px 32px; overflow-x: auto; }
  .notice { color: var(--ok); }
  button.link { border: none; background: none; padding: 0 2px; color: var(--accent); font: inherit; }
  button.link:hover { text-decoration: underline; }
</style>
