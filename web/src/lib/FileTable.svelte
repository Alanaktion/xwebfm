<script lang="ts">
  import type { Entry } from './api'
  import { humanSize, modeString, octal } from './format'

  let {
    entries,
    onopen,
    onaction,
  }: {
    entries: Entry[]
    onopen: (e: Entry) => void
    onaction: (action: 'chown' | 'chmod' | 'copy', e: Entry) => void
  } = $props()

  const icon: Record<string, string> = { dir: '📁', symlink: '🔗', file: '📄' }
  const fmtDate = (s: string) => new Date(s).toLocaleString()
</script>

<table>
  <thead>
    <tr>
      <th>Name</th>
      <th class="num">Size</th>
      <th>Mode</th>
      <th>Owner</th>
      <th>Modified</th>
      <th></th>
    </tr>
  </thead>
  <tbody>
    {#each entries as e (e.path)}
      <tr>
        <td class="name">
          <span aria-hidden="true">{icon[e.type] ?? '•'}</span>
          {#if e.type === 'dir' || e.type === 'symlink'}
            <button class="link" onclick={() => onopen(e)}>{e.name}</button>
          {:else}
            {e.name}
          {/if}
          {#if e.linkTarget}<span class="muted">→ {e.linkTarget}</span>{/if}
        </td>
        <td class="num">{e.type === 'dir' ? '' : humanSize(e.size)}</td>
        <td class="mono" title={octal(e.mode)}>{modeString(e.type, e.mode)}</td>
        <td class="mono">{e.user ?? e.uid}:{e.group ?? e.gid}</td>
        <td class="muted">{fmtDate(e.mtime)}</td>
        <td class="actions">
          <button onclick={() => onaction('chown', e)}>Owner</button>
          {#if e.type !== 'symlink'}<button onclick={() => onaction('chmod', e)}>Mode</button>{/if}
          {#if e.type === 'file'}<button onclick={() => onaction('copy', e)}>Copy</button>{/if}
        </td>
      </tr>
    {:else}
      <tr><td colspan="6" class="muted empty">Empty directory</td></tr>
    {/each}
  </tbody>
</table>

<style>
  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; font-weight: 500; color: var(--muted); padding: 6px 10px; border-bottom: 1px solid var(--border); }
  td { padding: 5px 10px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  tbody tr:hover { background: var(--hover); }
  .num { text-align: right; }
  .name { width: 100%; white-space: normal; word-break: break-all; }
  .muted { color: var(--muted); }
  .empty { text-align: center; padding: 24px; }
  .actions { display: flex; gap: 4px; justify-content: flex-end; }
  .actions button { padding: 2px 8px; font-size: 12px; }
  button.link { border: none; background: none; padding: 0; color: var(--accent); font: inherit; text-align: left; }
  button.link:hover { text-decoration: underline; }
</style>
