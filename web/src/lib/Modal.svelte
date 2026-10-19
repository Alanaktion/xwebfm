<script lang="ts">
  import { onMount, type Snippet } from 'svelte'

  let {
    title,
    busy = false,
    error = '',
    submitLabel = 'Apply',
    onsubmit,
    onclose,
    children,
  }: {
    title: string
    busy?: boolean
    error?: string
    submitLabel?: string
    onsubmit: () => void
    onclose: () => void
    children: Snippet
  } = $props()

  let dialog: HTMLDialogElement

  onMount(() => {
    dialog.showModal()
  })
</script>

<dialog bind:this={dialog} onclose={onclose}>
  <form
    onsubmit={(e) => {
      e.preventDefault()
      onsubmit()
    }}
  >
    <h2>{title}</h2>
    {@render children()}
    {#if error}<p class="error">{error}</p>{/if}
    <div class="actions">
      <button type="button" onclick={() => dialog.close()}>Cancel</button>
      <button type="submit" class="primary" disabled={busy}>{submitLabel}</button>
    </div>
  </form>
</dialog>
