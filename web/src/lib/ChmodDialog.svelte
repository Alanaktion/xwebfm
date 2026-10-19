<script lang="ts">
  import { untrack } from 'svelte'
  import { api, type Entry, type Job } from './api'
  import { modeString, octal } from './format'
  import Modal from './Modal.svelte'

  let {
    entries,
    onclose,
    ondone,
    onqueued,
  }: {
    entries: Entry[]
    onclose: () => void
    ondone: (msg: string, error?: boolean) => void
    onqueued: (job: Job) => void
  } = $props()

  let label = $derived(entries.length === 1 ? entries[0].name : `${entries.length} items`)

  let mode = $state(untrack(() => octal(entries[0].mode)))
  // Presets set folders and files separately (e.g. 755 / 644).
  const presets = [
    { id: 'custom', label: 'Custom (same mode for everything)', dir: '', file: '' },
    { id: 'private', label: 'Private: owner only', dir: '700', file: '600' },
    { id: 'standard', label: 'Standard: others can read', dir: '755', file: '644' },
    { id: 'group', label: 'Group-writable', dir: '775', file: '664' },
    { id: 'shared', label: 'Group-shared (setgid folders)', dir: '2775', file: '664' },
    { id: 'readonly', label: 'Read-only for everyone', dir: '555', file: '444' },
  ]
  let presetId = $state('custom')
  let preset = $derived(presets.find((p) => p.id === presetId)!)
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

  /** The mode fields to send: one mode, or a folder/file pair from a preset. */
  function modeFields(): { mode?: string; dirMode?: string; fileMode?: string } {
    return preset.id === 'custom' ? { mode } : { dirMode: preset.dir, fileMode: preset.file }
  }

  async function submit() {
    if (preset.id === 'custom' && value === null) {
      error = 'Mode must be 1–4 octal digits'
      return
    }
    busy = true
    error = ''
    // Recursive changes can take a while, so they run as a background job.
    if (recursive && entries.some((e) => e.type === 'dir')) {
      try {
        onqueued(await api.jobs.submit({ kind: 'chmod', paths: entries.map((e) => e.path), ...modeFields(), recursive }))
      } catch (e) {
        error = (e as Error).message
      } finally {
        busy = false
      }
      return
    }
    const failures: string[] = []
    for (const e of entries) {
      try {
        await api.chmod({ path: e.path, ...modeFields(), recursive: recursive && e.type === 'dir' })
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
    ondone(`Mode of ${label} set to ${preset.id === 'custom' ? mode : `${preset.dir} (folders) / ${preset.file} (files)`}`)
  }
</script>

<Modal title="Change mode: {label}" {busy} {error} onsubmit={submit} {onclose}>
  <label for="preset">Preset</label>
  <select id="preset" bind:value={presetId} style="width: 100%">
    {#each presets as p}<option value={p.id}>{p.label}{p.dir ? ` — ${p.dir} / ${p.file}` : ''}</option>{/each}
  </select>

  {#if preset.id !== 'custom'}
    <p class="summary">
      Folders <code>{preset.dir}</code> ({modeString('dir', parseInt(preset.dir, 8))}) · files
      <code>{preset.file}</code> ({modeString('file', parseInt(preset.file, 8))})
    </p>
  {:else}
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
  {/if}

  {#if entries.some((e) => e.type === 'dir')}
    <label class="check"><input type="checkbox" bind:checked={recursive} /> Apply recursively (symlinks are skipped)</label>
  {/if}
</Modal>

<style>
  .preview { margin-left: 10px; }
  .summary { color: var(--muted); margin: 8px 0 0; }
  .grid { margin-top: 12px; border-collapse: collapse; }
  .grid th { text-align: left; padding-right: 14px; font-weight: 500; }
  .grid td { padding: 2px 10px 2px 0; }
  .grid label { margin: 0; }
</style>
