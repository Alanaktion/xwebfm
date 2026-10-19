<script lang="ts">
  import { COLUMNS, defaultColumns } from './columns'
  import Modal from './Modal.svelte'
  import { persist, settings } from './settings.svelte'

  let { onclose }: { onclose: () => void } = $props()

  function save() {
    persist('columns', settings.columns)
  }
</script>

<Modal title="Columns" {onclose}>
  <p class="muted">Shown in both panes and remembered in this browser. Name is always shown.</p>
  {#each COLUMNS as c (c.key)}
    <label class="check">
      <input type="checkbox" bind:checked={settings.columns[c.key]} onchange={save} />
      <span>{c.label} <span class="muted">— {c.help}</span></span>
    </label>
  {/each}
  <button
    type="button"
    class="reset"
    onclick={() => {
      settings.columns = defaultColumns()
      save()
    }}>Reset to defaults</button
  >
</Modal>

<style>
  .muted { color: var(--muted); font-size: 12px; }
  p.muted { margin: 0 0 6px; }
  label.check { margin: 6px 0; align-items: flex-start; }
  .reset { margin-top: 10px; font-size: 12px; padding: 2px 10px; }
</style>
