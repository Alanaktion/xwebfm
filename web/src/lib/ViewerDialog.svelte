<script lang="ts">
  import { onMount, untrack } from 'svelte'
  import { api, ApiError, type Entry } from './api'
  import { humanSize } from './format'
  import Modal from './Modal.svelte'
  import TextEditor from './editor/TextEditor.svelte'
  import type { EditorHandle, EditorInfo } from './editor/codemirror'
  import { loadSetting, persist } from './settings.svelte'

  let {
    entry: initial,
    edit: initialEdit = false,
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
  /** A byte order mark the file started with, kept on save. */
  let bom = $state('')
  let editor = $state<EditorHandle | null>(null)
  let info = $state<EditorInfo | null>(null)
  let language = $state<string | null>(null)
  let wrap = $state(loadSetting('editorWrap', false))
  // Bumped to recreate the editor with freshly loaded text.
  let generation = $state(0)
  let busy = $state(false)
  let error = $state('')
  let conflict = $state(false)

  let src = $derived(`${api.downloadUrl(entry.path)}&inline=1`)
  let dirty = $derived(editing && !!info?.dirty)
  let crlf = $derived(text.includes('\r\n'))

  onMount(load)

  async function load(edit = initialEdit) {
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
          decoded = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes)
        } catch {
          // A cut-off multi-byte character at the end is fine for viewing.
          decoded = truncated ? new TextDecoder('utf-8', { ignoreBOM: true }).decode(bytes) : null
        }
      }
      if (decoded === null) {
        kind = 'hex'
        hex = hexdump(bytes.subarray(0, HEX_LIMIT))
        truncated = entry.size > HEX_LIMIT
        if (edit) error = 'This is not UTF-8 text, so it can only be viewed.'
        return
      }
      bom = decoded.startsWith('\ufeff') ? '\ufeff' : ''
      text = decoded.slice(bom.length)
      kind = 'text'
      editing = false
      if (edit) {
        if (entry.size > EDIT_LIMIT) {
          error = `Files over ${humanSize(EDIT_LIMIT)} can't be edited here.`
        } else {
          editing = true
        }
      }
      generation++
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
      const content = editor!.getText()
      entry = await api.write({ path: entry.path, content: bom + content, expectSize: entry.size, expectMtime: entry.mtime })
      text = content
      editor!.markSaved()
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
    await load(editing)
  }

  /** Switches the viewer to editing, loading the rest of the file first if the view was cut short. */
  async function startEdit() {
    if (editing || kind !== 'text' || entry.size > EDIT_LIMIT) return
    if (truncated) {
      await load(true)
    } else {
      editing = true
    }
    editor?.focus()
  }

  function toggleWrap() {
    wrap = !wrap
    persist('editorWrap', wrap)
  }

  function close() {
    if (dirty && !confirm('Discard unsaved changes?')) return
    onclose()
  }

  function keydown(ev: KeyboardEvent) {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === 's' && editing) {
      ev.preventDefault()
      if (!busy && editor) save()
    } else if (ev.key === 'F4' && !editing) {
      ev.preventDefault()
      startEdit()
    } else if (ev.altKey && ev.code === 'KeyZ') {
      ev.preventDefault()
      toggleWrap()
    }
  }
</script>

<svelte:window onkeydown={keydown} />

<Modal
  title="{editing ? 'Edit' : 'View'}: {entry.name}"
  wide={kind !== 'text'}
  full={kind === 'text'}
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
      <button type="button" class="small" onclick={startEdit}>Edit (F4)</button>
    {/if}
    {#if kind === 'text'}
      <label class="small wrapopt" title="Wrap long lines (Alt+Z)">
        <input type="checkbox" checked={wrap} onchange={toggleWrap} /> Wrap
      </label>
    {/if}
    <a class="small" href={api.downloadUrl(entry.path)} download>Download</a>
  </p>

  <div class="content" class:code={kind === 'text'}>
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
    {:else}
      {#key generation}
        <TextEditor
          doc={text}
          filename={entry.name}
          readOnly={!editing}
          {wrap}
          autofocus
          bind:handle={editor}
          bind:info
          bind:language
        />
      {/key}
    {/if}
  </div>
  {#if kind === 'text'}
    <p class="status muted">
      {#if info}
        <span>Ln {info.line}, Col {info.col}{#if info.selected} ({info.selected} selected){/if}</span>
        <span>{info.lines.toLocaleString()} lines</span>
      {/if}
      <span>{language ?? 'Plain text'}</span>
      {#if editor}<span>{editor.indent}</span>{/if}
      <span>{crlf ? 'CRLF' : 'LF'}{bom ? ' · BOM' : ''}</span>
      {#if dirty}<span class="modified">Modified</span>{/if}
    </p>
    {#if editing}
      <p class="muted note">
        Ctrl+S saves, Ctrl+F finds and replaces, Alt+Z toggles wrapping, Esc leaves the editor's search or the dialog.
        Saving writes a new file with the same owner, mode and ACL and swaps it in, so other hard links keep the old
        contents.
      </p>
    {/if}
  {/if}
  {#if conflict}<button type="button" onclick={reload}>Reload from disk</button>{/if}
</Modal>

<style>
  .meta { margin: 0 0 8px; font-size: 12px; display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .muted { color: var(--muted); }
  .small { font-size: 12px; padding: 0 8px; }
  .content { max-height: 65vh; overflow: auto; border: 1px solid var(--border); border-radius: 6px; }
  img, video { max-width: 100%; max-height: 64vh; display: block; margin: 0 auto; }
  audio { width: 100%; margin: 12px 0; }
  pre { margin: 0; padding: 8px; font-size: 12px; line-height: 1.45; }
  .content.code { flex: 1; min-height: 120px; max-height: none; overflow: hidden; }
  .wrapopt { display: inline-flex; align-items: center; gap: 4px; cursor: pointer; }
  .status { display: flex; flex-wrap: wrap; gap: 4px 14px; font-size: 11px; margin: 4px 2px 0; }
  .status .modified { color: var(--warn); }
  .note { font-size: 11px; margin: 4px 2px; }
</style>
