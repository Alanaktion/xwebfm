<script lang="ts">
  // A CodeMirror view of a text file: read-only for viewing, or editable.
  // CodeMirror is loaded the first time this is shown.
  import { onMount } from 'svelte'
  import type { EditorHandle, EditorInfo } from './codemirror'

  let {
    doc,
    filename,
    readOnly = false,
    wrap = false,
    autofocus = false,
    handle = $bindable(null),
    info = $bindable(null),
    language = $bindable(null),
  }: {
    doc: string
    filename: string
    readOnly?: boolean
    wrap?: boolean
    autofocus?: boolean
    handle?: EditorHandle | null
    info?: EditorInfo | null
    /** The detected language's name, once loaded. */
    language?: string | null
  } = $props()

  let el: HTMLDivElement
  let error = $state('')

  onMount(() => {
    let h: EditorHandle | null = null
    let gone = false
    import('./codemirror').then(
      ({ createEditor }) => {
        if (gone) return
        h = createEditor(el, { doc, filename, readOnly, wrap, onupdate: (i) => (info = i) })
        handle = h
        h.language.then((l) => (language = l))
        if (autofocus) h.focus()
      },
      (e) => (error = `Couldn't load the editor: ${(e as Error).message}`),
    )
    return () => {
      gone = true
      h?.destroy()
      handle = null
    }
  })

  $effect(() => {
    handle?.setReadOnly(readOnly)
  })
  $effect(() => {
    handle?.setWrap(wrap)
  })
</script>

<div class="wrap">
  {#if error}<p class="error">{error}</p>{:else if !handle}<p class="muted loading">Loading editor…</p>{/if}
  <div class="editor" bind:this={el}></div>
</div>

<style>
  .wrap, .editor { height: 100%; min-height: 0; }
  .wrap { display: flex; flex-direction: column; }
  .editor { flex: 1; }
  .loading, .error { margin: 8px; font-size: 12px; }
  .muted { color: var(--muted); }
</style>
