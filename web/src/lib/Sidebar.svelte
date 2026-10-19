<script lang="ts">
  import type { Account } from './api'
  import { persist, settings } from './settings.svelte'

  let {
    users,
    current,
    onnavigate,
  }: {
    users: Account[]
    /** The active pane's directory. */
    current: string
    onnavigate: (path: string) => void
  } = $props()

  // Real people's homes: root plus regular UIDs (1000–59999), skipping
  // placeholder homes used by service and build accounts.
  const placeholder = new Set(['/', '/nonexistent', '/var/empty', '/dev/null', '/var/lib/nobody'])
  let homes = $derived(
    users
      .filter((u) => u.home && !placeholder.has(u.home) && (u.id === 0 || (u.id >= 1000 && u.id < 60000)))
      .sort((a, b) => a.name.localeCompare(b.name)),
  )

  function setBookmarks(list: string[]) {
    settings.bookmarks = list
    persist('bookmarks', list)
  }
</script>

<aside class="sidebar" aria-label="Places">
  <h2>Places</h2>
  <ul>
    <li><button class="link" class:here={current === '/'} onclick={() => onnavigate('/')}>/ (root)</button></li>
  </ul>

  <h2>Homes</h2>
  <ul>
    {#each homes as u (u.id)}
      <li>
        <button class="link" class:here={current === u.home} title={u.home} onclick={() => onnavigate(u.home!)}>
          {u.name}
        </button>
      </li>
    {:else}
      <li class="muted">No user homes</li>
    {/each}
  </ul>

  <h2>
    Bookmarks
    <button
      class="add"
      title="Bookmark {current}"
      disabled={settings.bookmarks.includes(current)}
      onclick={() => setBookmarks([...settings.bookmarks, current])}>+</button
    >
  </h2>
  <ul>
    {#each settings.bookmarks as b (b)}
      <li class="bm">
        <button class="link mono" class:here={current === b} title={b} onclick={() => onnavigate(b)}>
          {b === '/' ? '/' : b.slice(b.lastIndexOf('/') + 1)}
        </button>
        <button class="remove" title="Remove bookmark" onclick={() => setBookmarks(settings.bookmarks.filter((x) => x !== b))}
          >×</button
        >
      </li>
    {:else}
      <li class="muted">Use + to bookmark the current folder</li>
    {/each}
  </ul>
</aside>

<style>
  .sidebar {
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 6px 4px;
    overflow: auto;
    min-height: 0;
    font-size: 13px;
  }
  h2 {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
    margin: 10px 8px 4px;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  ul { list-style: none; margin: 0; padding: 0; }
  li { display: flex; align-items: center; }
  li.muted { color: var(--muted); padding: 2px 8px; font-size: 12px; }
  button.link {
    flex: 1;
    min-width: 0;
    text-align: left;
    border: none;
    background: none;
    color: var(--fg);
    padding: 3px 8px;
    border-radius: 5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font: inherit;
  }
  button.link:hover { background: var(--hover); }
  button.link.here { background: var(--selected); }
  .add, .remove { border: none; background: none; padding: 0 6px; color: var(--muted); font-size: 14px; }
  .remove { visibility: hidden; }
  li.bm:hover .remove { visibility: visible; }
</style>
