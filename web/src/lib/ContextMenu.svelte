<script lang="ts" module>
  /** A context menu entry, or 'sep' for a divider. */
  export type MenuItem =
    | {
        label: string
        /** Keyboard shortcut shown on the right. */
        hint?: string
        danger?: boolean
        run: () => void
      }
    | 'sep'
</script>

<script lang="ts">
  import { onMount } from 'svelte'

  let {
    x,
    y,
    label,
    items,
    onclose,
  }: {
    /** Viewport position to open at; the menu flips to stay on screen. */
    x: number
    y: number
    /** Accessible name, e.g. "3 items". */
    label: string
    items: MenuItem[]
    onclose: () => void
  } = $props()

  let menu: HTMLDivElement
  let left = $state(0)
  let top = $state(0)
  // Dividers at the ends or doubled up (when a group has nothing) are dropped.
  let shown = $derived(
    items.filter((it, i, all) => it !== 'sep' || (i > 0 && i < all.length - 1 && all[i + 1] !== 'sep')),
  )

  onMount(() => {
    const previous = document.activeElement as HTMLElement | null
    const r = menu.getBoundingClientRect()
    const margin = 4
    left = x + r.width > innerWidth - margin ? Math.max(margin, x - r.width) : x
    top = y + r.height > innerHeight - margin ? Math.max(margin, y - r.height) : y
    buttons()[0]?.focus()

    const outside = (ev: Event) => {
      if (!menu.contains(ev.target as Node)) onclose()
    }
    // Capture, so a click elsewhere closes the menu before it does anything else.
    addEventListener('pointerdown', outside, true)
    addEventListener('resize', onclose)
    addEventListener('blur', onclose)
    addEventListener('scroll', outside, true)
    return () => {
      removeEventListener('pointerdown', outside, true)
      removeEventListener('resize', onclose)
      removeEventListener('blur', onclose)
      removeEventListener('scroll', outside, true)
      if (previous?.isConnected) previous.focus()
    }
  })

  const buttons = () => [...menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]

  function key(ev: KeyboardEvent) {
    // Keys stay inside the menu; the app's global shortcuts don't see them.
    ev.stopPropagation()
    const all = buttons()
    const i = all.indexOf(document.activeElement as HTMLButtonElement)
    const focus = (n: number) => all[(n + all.length) % all.length]?.focus()
    switch (ev.key) {
      case 'ArrowDown':
        focus(i + 1)
        break
      case 'ArrowUp':
        focus(i < 0 ? -1 : i - 1)
        break
      case 'Home':
      case 'PageUp':
        focus(0)
        break
      case 'End':
      case 'PageDown':
        focus(-1)
        break
      case 'Escape':
      case 'Tab':
      case 'ContextMenu':
        onclose()
        break
      case 'F10':
        if (!ev.shiftKey) return
        onclose()
        break
      default: {
        // A letter jumps to the next item starting with it.
        if (ev.key.length !== 1 || ev.ctrlKey || ev.metaKey || ev.altKey || ev.key === ' ') return
        const k = ev.key.toLowerCase()
        const order = [...all.slice(i + 1), ...all.slice(0, i + 1)]
        order.find((b) => b.dataset.label?.toLowerCase().startsWith(k))?.focus()
      }
    }
    ev.preventDefault()
  }

  function activate(it: Exclude<MenuItem, 'sep'>) {
    onclose()
    it.run()
  }
</script>

<!-- svelte-ignore a11y_interactive_supports_focus -->
<div
  class="contextmenu"
  role="menu"
  aria-label={label}
  tabindex="-1"
  bind:this={menu}
  style:left="{left}px"
  style:top="{top}px"
  onkeydown={key}
  oncontextmenu={(e) => e.preventDefault()}
>
  {#each shown as it}
    {#if it === 'sep'}
      <div class="sep" role="separator"></div>
    {:else}
      <button
        type="button"
        role="menuitem"
        tabindex="-1"
        class:danger={it.danger}
        data-label={it.label}
        onclick={() => activate(it)}
        onpointerenter={(e) => (e.currentTarget as HTMLElement).focus()}
      >
        <span class="label" title={it.label}>{it.label}</span>
        {#if it.hint}<kbd>{it.hint}</kbd>{/if}
      </button>
    {/if}
  {/each}
</div>

<style>
  .contextmenu {
    position: fixed;
    z-index: 20;
    display: flex;
    flex-direction: column;
    min-width: 200px;
    max-height: calc(100vh - 8px);
    max-height: calc(100dvh - 8px);
    overflow-y: auto;
    padding: 4px;
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    box-shadow: 0 4px 12px rgb(0 0 0 / 0.15);
    outline: none;
  }
  button {
    display: flex;
    justify-content: space-between;
    gap: 24px;
    border: none;
    text-align: left;
    white-space: nowrap;
    padding: 4px 10px;
  }
  button:focus { outline: none; background: var(--selected); }
  button:hover { background: var(--selected); }
  button.danger { color: var(--danger); }
  .label { max-width: 360px; overflow: hidden; text-overflow: ellipsis; }
  kbd { font: 11px ui-monospace, monospace; color: var(--muted); }
  .sep { height: 1px; margin: 4px 2px; background: var(--border); }
</style>
