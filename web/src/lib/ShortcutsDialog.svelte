<script lang="ts">
  import Modal from './Modal.svelte'
  import { SHORTCUTS } from './shortcuts'

  let { onclose }: { onclose: () => void } = $props()

  // Keys starting with a modifier are a combination; otherwise alternatives.
  const combo = (keys: string[]) => ['Ctrl', 'Shift', 'Alt'].includes(keys[0])
</script>

<Modal title="Keyboard shortcuts" wide {onclose}>
  <div class="groups">
    {#each SHORTCUTS as g}
      <section>
        <h3>{g.group}</h3>
        <table>
          <tbody>
            {#each g.keys as [keys, what]}
              <tr>
                <td class="keys">
                  {#each keys as k, i}{#if i > 0}<span class="sep">{combo(keys) ? '+' : '/'}</span>{/if}<kbd>{k}</kbd>{/each}
                </td>
                <td>{what}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </section>
    {/each}
  </div>
  <p class="muted">Drag rows to copy or move: within a pane moves, between panes copies; hold Ctrl to copy or Shift to move.</p>
</Modal>

<style>
  .groups { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 4px 24px; }
  h3 { font-size: 13px; margin: 10px 0 4px; color: var(--muted); font-weight: 500; }
  table { border-collapse: collapse; width: 100%; }
  td { padding: 3px 0; vertical-align: top; font-size: 13px; }
  td.keys { white-space: nowrap; padding-right: 14px; width: 1%; }
  kbd {
    display: inline-block;
    font: 11px ui-monospace, monospace;
    padding: 1px 5px;
    border: 1px solid var(--border);
    border-bottom-width: 2px;
    border-radius: 4px;
    background: var(--hover);
  }
  .sep { color: var(--muted); font-size: 11px; margin: 0 2px; }
  .muted { color: var(--muted); font-size: 12px; margin-top: 14px; }
</style>
