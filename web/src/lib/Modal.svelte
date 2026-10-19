<script lang="ts">
  import { onMount, type Snippet } from 'svelte'

  let {
    title,
    busy = false,
    error = '',
    submitLabel = 'Apply',
    danger = false,
    wide = false,
    onsubmit,
    onclose,
    children,
  }: {
    title: string
    busy?: boolean
    error?: string
    submitLabel?: string
    danger?: boolean
    /** Wider dialog for content-heavy views. */
    wide?: boolean
    /** Without onsubmit the dialog only has a Close button. */
    onsubmit?: () => void
    onclose: () => void
    children: Snippet
  } = $props()

  let dialog: HTMLDialogElement

  onMount(() => {
    dialog.showModal()
  })
</script>

<dialog bind:this={dialog} onclose={onclose} class:wide>
  <form
    onsubmit={(e) => {
      e.preventDefault()
      onsubmit?.()
    }}
  >
    <h2>{title}</h2>
    {@render children()}
    {#if error}<p class="error" style="white-space: pre-line">{error}</p>{/if}
    <div class="actions">
      {#if onsubmit}
        <button type="button" onclick={() => dialog.close()}>Cancel</button>
        <button type="submit" class="primary" class:danger disabled={busy}>{submitLabel}</button>
      {:else}
        <button type="button" onclick={() => dialog.close()}>Close</button>
      {/if}
    </div>
  </form>
</dialog>

<style>
  dialog.wide { width: min(760px, calc(100vw - 32px)); }
</style>
