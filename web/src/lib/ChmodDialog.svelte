<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Entry } from './api'
  import { modeString, octal } from './format'
  import Modal from './Modal.svelte'

  let {
    entries,
    onclose,
    ondone,
  }: { entries: Entry[]; onclose: () => void; ondone: (msg: string, error?: boolean) => void } = $props()

  let label = $derived(entries.length === 1 ? entries[0].name : `${entries.length} items`)

  let mode = $state(untrack(() => octal(entries[0].mode)))
  let recursive = $state(false)
  let busy = $state(false)
  let error = $state('')

  // Bits shown as a checkbox grid: [label, bit] per class.
  const classes = [
    ['Owner', 6],
    ['Group', 3],
    ['Other', 0],
  ] as const
  const perms = [
    ['r', 4],
    ['w', 2],
    ['x', 1],
  ] as const
  const specials = [
    ['setuid', 0o4000],
    ['setgid', 0o2000],
    ['sticky', 0o1000],
  ] as const

  let value = $derived(/^[0-7]{1,4}$/.test(mode) ? parseInt(mode, 8) : null)

  function toggle(bit: number) {
    if (value === null) return
    mode = octal(value ^ bit)
  }

  async function submit() {
    if (value === null) {
      error = 'Mode must be 1–4 octal digits'
      return
    }
    busy = true
    error = ''
    const failures: string[] = []
    for (const e of entries) {
      try {
        await api.chmod({ path: e.path, mode, recursive: recursive && e.type === 'dir' })
      } catch (err) {
        failures.push(`${e.name}: ${(err as Error).message}`)
      }
    }
    busy = false
    if (failures.length) {
      error = failures.join('\n')
      if (failures.length < entries.length) ondone(`Changed ${entries.length - failures.length} of ${entries.length}`, true)
      return
    }
    ondone(`Mode of ${label} set to ${mode}`)
  }
</script>

<Modal title="Change mode: {label}" {busy} {error} onsubmit={submit} {onclose}>
  <label for="mode">Octal mode</label>
  <input id="mode" class="mono" bind:value={mode} size="6" autocomplete="off" />
  {#if value !== null}<code class="preview">{modeString(entries[0].type, value)}</code>{/if}

  <table class="grid">
    <tbody>
      {#each classes as [label, shift]}
        <tr>
          <th>{label}</th>
          {#each perms as [p, bit]}
            <td>
              <label class="check">
                <input
                  type="checkbox"
                  checked={value !== null && (value & (bit << shift)) !== 0}
                  onchange={() => toggle(bit << shift)}
                />{p}
              </label>
            </td>
          {/each}
        </tr>
      {/each}
      <tr>
        <th>Special</th>
        {#each specials as [label, bit]}
          <td>
            <label class="check">
              <input type="checkbox" checked={value !== null && (value & bit) !== 0} onchange={() => toggle(bit)} />{label}
            </label>
          </td>
        {/each}
      </tr>
    </tbody>
  </table>

  {#if entries.some((e) => e.type === 'dir')}
    <label class="check"><input type="checkbox" bind:checked={recursive} /> Apply recursively (symlinks are skipped)</label>
  {/if}
</Modal>

<style>
  .preview { margin-left: 10px; }
  .grid { margin-top: 12px; border-collapse: collapse; }
  .grid th { text-align: left; padding-right: 14px; font-weight: 500; }
  .grid td { padding: 2px 10px 2px 0; }
  .grid label { margin: 0; }
</style>
