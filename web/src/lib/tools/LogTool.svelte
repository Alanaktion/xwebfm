<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte'
  import Modal from '../Modal.svelte'
  import { humanSize } from '../format'
  import type { ToolProps } from '.'
  import { logFile, type LogFilter, type LogLevel, type LogLine, type LogPage } from './log'

  let { entry, onclose }: ToolProps = $props()

  const PAGE = 1000
  /** Lines kept in memory; following drops the oldest beyond this. */
  const MAX_LINES = 20000
  const POLL_MS = 1000
  // svelte-ignore state_referenced_locally
  const log = logFile(entry.path)

  let lines = $state<LogLine[]>([])
  /** Where earlier lines continue (0: start of file reached). */
  let start = $state(0)
  /** Where following continues. */
  let end = 0
  let inode = 0
  // svelte-ignore state_referenced_locally
  let size = $state(entry.size)
  /** The newest lines were dropped to load earlier ones, so following can't continue. */
  let cutEnd = $state(false)
  let loading = $state(false)
  let error = $state('')
  let info = $state('')

  let filterText = $state('')
  let regex = $state(false)
  let matchCase = $state(false)
  let invert = $state(false)
  let level = $state<LogLevel | ''>('')
  let follow = $state(true)
  let wrap = $state(false)

  let search = $state('')
  let current = $state(-1) // index into hits

  let scroller: HTMLDivElement
  let searchInput: HTMLInputElement
  /** Whether the view is scrolled to the bottom, so new lines keep it there. */
  let pinned = true
  let gen = 0 // bumped on reload so stale responses are ignored
  let timer: ReturnType<typeof setTimeout> | undefined
  let filterTimer: ReturnType<typeof setTimeout> | undefined

  let filter = $derived<LogFilter>({ text: filterText, regex, case: matchCase, not: invert && !!filterText, level })
  let filtered = $derived(!!filterText || !!level)

  let needle = $derived(search.toLowerCase())
  let hits = $derived.by(() => {
    if (!needle) return [] as number[]
    const out: number[] = []
    lines.forEach((l, i) => {
      if (l.t.toLowerCase().includes(needle)) out.push(i)
    })
    return out
  })
  let currentLine = $derived(current >= 0 && current < hits.length ? hits[current] : -1)

  onMount(() => {
    reload()
  })
  onDestroy(() => {
    gen++
    clearTimeout(timer)
    clearTimeout(filterTimer)
  })

  async function reload() {
    const g = ++gen
    clearTimeout(timer)
    loading = true
    error = ''
    try {
      const page = await log.tail({ lines: PAGE, filter })
      if (g !== gen) return
      lines = page.lines
      start = page.start
      apply(page)
      cutEnd = false
      current = -1
      pinned = true
      await tick()
      scrollToEnd()
    } catch (e) {
      if (g === gen) error = (e as Error).message
    } finally {
      if (g === gen) {
        loading = false
        schedule()
      }
    }
  }

  function apply(page: LogPage) {
    end = page.end
    inode = page.inode
    size = page.size
  }

  let earlier: Promise<void> | null = null

  /** Loads the page before the first line; concurrent calls share one load. */
  function loadEarlier(): Promise<void> {
    if (earlier) return earlier
    if (loading || start <= 0) return Promise.resolve()
    earlier = readEarlier().finally(() => (earlier = null))
    return earlier
  }

  async function readEarlier() {
    const g = gen
    loading = true
    error = ''
    try {
      const page = await log.tail({ before: start, lines: PAGE, filter })
      if (g !== gen) return
      const before = scroller.scrollHeight - scroller.scrollTop
      const keep = currentLine
      let next = [...page.lines, ...lines]
      if (next.length > MAX_LINES) {
        next = next.slice(0, MAX_LINES)
        cutEnd = true
        follow = false
      }
      lines = next
      start = page.start
      // Keep the current match on the same line.
      if (keep >= 0) current = hits.indexOf(keep + page.lines.length)
      await tick()
      scroller.scrollTop = scroller.scrollHeight - before
      if (!page.lines.length && start > 0) info = `Scanned back to ${humanSize(start)} without a match`
    } catch (e) {
      if (g === gen) error = (e as Error).message
    } finally {
      if (g === gen) loading = false
    }
  }

  function schedule() {
    clearTimeout(timer)
    if (follow && !cutEnd) timer = setTimeout(poll, POLL_MS)
  }

  async function poll() {
    const g = gen
    if (document.hidden || loading) {
      schedule()
      return
    }
    try {
      let page: LogPage
      do {
        page = await log.follow({ after: end, inode, lines: PAGE, filter })
        if (g !== gen) return
        if (page.reset) {
          info = 'The file was truncated or replaced; reloaded it'
          reload()
          return
        }
        // A partial last line comes again once it's complete (or longer).
        const keep = lines.filter((l) => l.o < end)
        let next = page.lines.length || keep.length !== lines.length ? [...keep, ...page.lines] : lines
        if (next.length > MAX_LINES) {
          const drop = next.length - MAX_LINES
          next = next.slice(drop)
          start = next[0].o
          current = -1
        }
        lines = next
        apply(page)
      } while (page.more && follow)
      if (pinned) {
        await tick()
        scrollToEnd()
      }
      if (error) error = ''
    } catch (e) {
      if (g === gen) error = (e as Error).message
    } finally {
      if (g === gen) schedule()
    }
  }

  function scrollToEnd() {
    if (scroller) scroller.scrollTop = scroller.scrollHeight
  }

  function onscroll() {
    pinned = scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 4
    if (scroller.scrollTop < 40 && start > 0 && !loading) loadEarlier()
  }

  function toggleFollow() {
    follow = !follow
    if (follow) {
      if (cutEnd) reload()
      else {
        pinned = true
        scrollToEnd()
        poll()
      }
    } else clearTimeout(timer)
  }

  function jumpToEnd() {
    if (cutEnd) reload()
    else {
      pinned = true
      scrollToEnd()
    }
  }

  function filterChanged() {
    clearTimeout(filterTimer)
    filterTimer = setTimeout(reload, 300)
  }

  /** Steps to the previous (older, -1) or next (newer, 1) match, loading earlier lines as needed. */
  async function findNext(dir: 1 | -1) {
    if (!needle) return
    info = ''
    if (!hits.length) {
      for (let i = 0; i < 20 && !hits.length && start > 0; i++) await loadEarlier()
      if (!hits.length) return
      current = hits.length - 1
    } else if (current < 0) {
      current = hits.length - 1
    } else if (current + dir < 0) {
      for (let i = 0; i < 20 && current === 0 && start > 0; i++) await loadEarlier()
      if (current > 0) current--
      else info = 'No earlier match'
    } else if (current + dir >= hits.length) {
      current = hits.length - 1
      info = 'No later match'
    } else current += dir
    await tick()
    scroller.querySelector('.line.current')?.scrollIntoView({ block: 'center' })
    pinned = false
  }

  function searchKey(ev: KeyboardEvent) {
    if (ev.key === 'Enter') {
      ev.preventDefault()
      findNext(ev.shiftKey ? 1 : -1)
    }
  }

  function keydown(ev: KeyboardEvent) {
    if ((ev.ctrlKey || ev.metaKey) && ev.key === 'f') {
      ev.preventDefault()
      searchInput.focus()
      searchInput.select()
    }
  }

  function parts(text: string): { t: string; hit: boolean }[] {
    if (!needle) return [{ t: text, hit: false }]
    const lower = text.toLowerCase()
    const out: { t: string; hit: boolean }[] = []
    let i = 0
    for (let j = lower.indexOf(needle); j >= 0; j = lower.indexOf(needle, j + needle.length)) {
      if (j > i) out.push({ t: text.slice(i, j), hit: false })
      out.push({ t: text.slice(j, j + needle.length), hit: true })
      i = j + needle.length
    }
    if (i < text.length) out.push({ t: text.slice(i), hit: false })
    return out
  }

</script>

<svelte:window onkeydown={keydown} />

<Modal title="Log: {entry.name}" full {error} {onclose}>
  <div class="logtool">
    <div class="bar">
      <input
        class="filter"
        type="search"
        placeholder={regex ? 'Filter (regular expression)' : 'Filter lines'}
        bind:value={filterText}
        oninput={filterChanged}
        spellcheck="false"
      />
      <button type="button" class:on={regex} onclick={() => ((regex = !regex), filterText && reload())} title="Regular expression" aria-pressed={regex}>.*</button>
      <button type="button" class:on={matchCase} onclick={() => ((matchCase = !matchCase), filterText && reload())} title="Match case" aria-pressed={matchCase}>Aa</button>
      <button type="button" class:on={invert} onclick={() => ((invert = !invert), filterText && reload())} title="Hide matching lines instead" aria-pressed={invert}>Not</button>
      <select bind:value={level} onchange={reload} title="Minimum level">
        <option value="">All levels</option>
        <option value="error">Errors</option>
        <option value="warn">Warnings +</option>
        <option value="info">Info +</option>
        <option value="debug">Debug +</option>
      </select>
      <span class="spacer"></span>
      <span class="find">
        <input
          bind:this={searchInput}
          type="search"
          placeholder="Search (Ctrl+F)"
          bind:value={search}
          oninput={() => (current = -1)}
          onkeydown={searchKey}
          spellcheck="false"
        />
        {#if needle}<span class="small muted count">{hits.length ? `${current >= 0 ? current + 1 : '–'}/${hits.length}` : 'none'}</span>{/if}
        <button type="button" onclick={() => findNext(-1)} disabled={!needle} title="Previous match (Enter)" aria-label="Previous match">↑</button>
        <button type="button" onclick={() => findNext(1)} disabled={!needle} title="Next match (Shift+Enter)" aria-label="Next match">↓</button>
      </span>
      <button type="button" class:on={wrap} onclick={() => (wrap = !wrap)} aria-pressed={wrap} title="Wrap long lines">Wrap</button>
      <button type="button" class:on={follow} class="follow" onclick={toggleFollow} aria-pressed={follow} title="Show new lines as they're written">
        {follow ? '● Following' : 'Follow'}
      </button>
    </div>

    <div class="view mono" class:wrap bind:this={scroller} {onscroll}>
      {#if start > 0}
        <button type="button" class="earlier" onclick={loadEarlier} disabled={loading}>
          {loading ? 'Loading…' : `Load earlier lines (${humanSize(start)} before)`}
        </button>
      {:else if lines.length}
        <div class="edge muted small">Start of file</div>
      {/if}
      {#each lines as line, i (line.o)}
        <div class="line {line.l ?? ''}" class:current={i === currentLine} class:partial={line.partial}>
          {#each parts(line.t) as p}{#if p.hit}<mark>{p.t}</mark>{:else}{p.t}{/if}{/each}{#if line.cut}<span class="muted"> … (cut)</span>{/if}
        </div>
      {:else}
        {#if !loading}<div class="edge muted small">{filtered ? 'No matching lines' : 'The file is empty'}</div>{/if}
      {/each}
    </div>

    <div class="status small muted">
      <span class="mono path" title={entry.path}>{entry.path}</span>
      <span>{humanSize(size)}</span>
      <span>{lines.length.toLocaleString()} line{lines.length === 1 ? '' : 's'}{filtered ? ' matching' : ''}</span>
      {#if info}<span class="notice" title={info}>{info}</span>{/if}
      <span class="spacer"></span>
      {#if cutEnd}<span class="warn">Newer lines were unloaded</span>{/if}
      <button type="button" onclick={jumpToEnd} title="Scroll to the newest line">End ⇣</button>
    </div>
  </div>
</Modal>

<style>
  .logtool { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 6px; }
  .bar { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .bar button { padding: 2px 8px; }
  .bar input { padding: 2px 6px; }
  .filter { flex: 0 1 260px; min-width: 120px; }
  .find { display: inline-flex; gap: 4px; align-items: center; }
  .find input { width: 180px; }
  .count { min-width: 4ch; text-align: right; }
  button.on { border-color: var(--accent); color: var(--accent); }
  .follow.on { background: var(--selected); }
  .spacer { flex: 1; }
  .view {
    flex: 1;
    min-height: 0;
    overflow: auto;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    font-size: 12px;
    line-height: 18px;
    padding: 4px 0;
  }
  .line {
    white-space: pre;
    padding: 0 8px 0 6px;
    border-left: 3px solid transparent;
    content-visibility: auto;
    contain-intrinsic-size: auto 18px;
    width: max-content;
    min-width: 100%;
    box-sizing: border-box;
  }
  .wrap .line { white-space: pre-wrap; overflow-wrap: anywhere; width: auto; }
  .line:hover { background: var(--hover); }
  .line.error { color: var(--danger); border-left-color: var(--danger); }
  .line.warn { color: var(--warn); border-left-color: var(--warn); }
  .line.debug,
  .line.trace { color: var(--muted); }
  .line.partial { opacity: 0.75; }
  .line.current { background: var(--selected); }
  mark { background: var(--cm-match); color: inherit; border-radius: 2px; }
  .line.current mark { background: var(--cm-match-current); }
  .earlier { display: block; margin: 2px auto 6px; padding: 2px 12px; font-size: 12px; }
  .edge { text-align: center; padding: 2px 0 6px; font-family: inherit; }
  .status { display: flex; gap: 12px; align-items: center; white-space: nowrap; }
  .status .path { overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .status button { padding: 0 8px; }
  .notice { color: var(--ok); overflow: hidden; text-overflow: ellipsis; }
  .warn { color: var(--warn); }
  .small { font-size: 12px; }
  .muted { color: var(--muted); }
</style>
