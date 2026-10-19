<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api, ApiError, type Entry } from './api'
  import { humanSize } from './format'
  import Modal from './Modal.svelte'

  let {
    entry: initial,
    edit = false,
    onclose,
    ondone,
  }: {
    entry: Entry
    /** Open in the editor instead of the viewer. */
    edit?: boolean
    onclose: () => void
    ondone: (msg: string) => void
  } = $props()

  const VIEW_LIMIT = 2 << 20 // text shown in the viewer
  const HEX_LIMIT = 64 << 10
  const EDIT_LIMIT = 5 << 20 // matches the server

  const ext = (n: string) => n.slice(n.lastIndexOf('.') + 1).toLowerCase()
  const media: Record<string, 'image' | 'audio' | 'video'> = {
    png: 'image', jpg: 'image', jpeg: 'image', gif: 'image', webp: 'image', avif: 'image', bmp: 'image',
    mp3: 'audio', ogg: 'audio', oga: 'audio', wav: 'audio', flac: 'audio', m4a: 'audio', opus: 'audio',
    mp4: 'video', webm: 'video', ogv: 'video', mov: 'video', mkv: 'video',
  }

  let entry = $state(untrack(() => initial))
  let kind = $state<'loading' | 'text' | 'hex' | 'image' | 'audio' | 'video'>('loading')
  let text = $state('')
  let hex = $state('')
  let truncated = $state(false)
  let editing = $state(false)
  let draft = $state('')
  let busy = $state(false)
  let error = $state('')
  let conflict = $state(false)

  let src = $derived(`${api.downloadUrl(entry.path)}&inline=1`)
  let dirty = $derived(editing && draft !== text)

  onMount(load)

  async function load() {
    error = ''
    conflict = false
    const m = media[ext(entry.name)]
    if (m && !edit) {
      kind = m
      return
    }
    const limit = edit ? EDIT_LIMIT : VIEW_LIMIT
    try {
      const res = await fetch(src, { headers: { Range: `bytes=0-${limit - 1}` } })
      if (!res.ok && res.status !== 206) throw new Error(`${res.status} ${res.statusText}`)
      const bytes = new Uint8Array(await res.arrayBuffer())
      truncated = entry.size > bytes.length
      const binary = bytes.subarray(0, 8192).includes(0)
      let decoded: string | null = null
      if (!binary) {
        try {
          decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
        } catch {
          // A cut-off multi-byte character at the end is fine for viewing.
          decoded = truncated ? new TextDecoder().decode(bytes) : null
        }
      }
      if (decoded === null) {
        kind = 'hex'
        hex = hexdump(bytes.subarray(0, HEX_LIMIT))
        truncated = entry.size > HEX_LIMIT
        if (edit) error = 'This is not UTF-8 text, so it can only be viewed.'
        return
      }
      kind = 'text'
      text = decoded
      if (edit) {
        if (entry.size > EDIT_LIMIT) {
          error = `Files over ${humanSize(EDIT_LIMIT)} can't be edited here.`
        } else {
          editing = true
          draft = text
        }
      }
    } catch (e) {
      error = (e as Error).message
    }
  }

  function hexdump(b: Uint8Array): string {
    const lines: string[] = []
    for (let off = 0; off < b.length; off += 16) {
      const row = b.subarray(off, off + 16)
      const hx = [...row].map((x) => x.toString(16).padStart(2, '0')).join(' ')
      const asc = [...row].map((x) => (x >= 32 && x < 127 ? String.fromCharCode(x) : '.')).join('')
      lines.push(`${off.toString(16).padStart(8, '0')}  ${hx.padEnd(47)}  ${asc}`)
    }
    return lines.join('\n')
  }

  async function save() {
    busy = true
    error = ''
    try {
      entry = await api.write({ path: entry.path, content: draft, expectSize: entry.size, expectMtime: entry.mtime })
      text = draft
      ondone(`Saved ${entry.name}`)
    } catch (e) {
      conflict = e instanceof ApiError && e.status === 409
      error = conflict
        ? 'The file changed on disk since it was opened. Reload to see the new version (your edits will be lost), or copy your changes first.'
        : (e as Error).message
    } finally {
      busy = false
    }
  }

  async function reload() {
    entry = await api.stat(entry.path)
    editing = false
    await load()
  }

  function close() {
    if (dirty && !confirm('Discard unsaved changes?')) return
    onclose()
  }

  function keydown(ev: KeyboardEvent) {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === 's' && editing) {
      ev.preventDefault()
      if (!busy) save()
    }
  }
</script>

<Modal
  title="{editing ? 'Edit' : 'View'}: {entry.name}"
  wide
  {error}
  {busy}
  submitLabel="Save"
  onsubmit={editing ? save : undefined}
  onclose={close}
>
  <p class="meta muted">
    <span class="mono">{entry.path}</span> · {humanSize(entry.size)}
    {#if truncated}· showing the first {humanSize(kind === 'hex' ? HEX_LIMIT : VIEW_LIMIT)}{/if}
    {#if !editing && kind === 'text' && entry.size <= EDIT_LIMIT}
      <button type="button" class="small" onclick={() => ((editing = true), (draft = text))}>Edit (F4)</button>
    {/if}
    <a class="small" href={api.downloadUrl(entry.path)} download>Download</a>
  </p>

  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="content" onkeydown={keydown}>
    {#if kind === 'loading'}
      <p class="muted">Loading…</p>
    {:else if kind === 'image'}
      <img {src} alt={entry.name} />
    {:else if kind === 'audio'}
      <audio controls {src}></audio>
    {:else if kind === 'video'}
      <!-- svelte-ignore a11y_media_has_caption -->
      <video controls {src}></video>
    {:else if kind === 'hex'}
      <pre class="mono hex">{hex}</pre>
    {:else if editing}
      <!-- svelte-ignore a11y_autofocus -->
      <textarea class="mono" bind:value={draft} spellcheck="false" autofocus></textarea>
      <p class="muted note">
        Ctrl+S saves. Saving writes a new file with the same owner, mode and ACL and swaps it in, so other hard
        links keep the old contents.
      </p>
    {:else}
      <ol class="mono text">
        {#each text.split('\n') as line}<li>{line || ' '}</li>{/each}
      </ol>
    {/if}
  </div>
  {#if conflict}<button type="button" onclick={reload}>Reload from disk</button>{/if}
</Modal>

<style>
  .meta { margin: 0 0 8px; font-size: 12px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .muted { color: var(--muted); }
  .small { font-size: 12px; padding: 0 8px; }
  .content { max-height: 65vh; overflow: auto; border: 1px solid var(--border); border-radius: 6px; }
  img, video { max-width: 100%; max-height: 64vh; display: block; margin: 0 auto; }
  audio { width: 100%; margin: 12px 0; }
  pre, ol { margin: 0; padding: 8px; font-size: 12px; line-height: 1.45; }
  ol.text { padding-left: 4.5em; white-space: pre-wrap; word-break: break-all; }
  ol.text li::marker { color: var(--muted); font-size: 11px; }
  textarea {
    width: 100%;
    height: 58vh;
    border: none;
    padding: 8px;
    font-size: 12px;
    line-height: 1.45;
    resize: vertical;
    tab-size: 4;
  }
  .note { font-size: 11px; margin: 4px 8px; }
</style>
