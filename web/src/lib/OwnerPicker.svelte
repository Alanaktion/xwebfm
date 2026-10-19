<script lang="ts" module>
  import type { OwnerFields, OwnerPolicy } from './api'

  /** Converts picker values to API fields; numeric input is treated as an ID. */
  export function ownerFields(policy: OwnerPolicy, user: string, group: string): OwnerFields {
    if (policy !== 'set') return { owner: policy }
    const f: OwnerFields = { owner: 'set' }
    const u = user.trim()
    const g = group.trim()
    if (/^\d+$/.test(u)) f.uid = Number(u)
    else if (u) f.user = u
    if (/^\d+$/.test(g)) f.gid = Number(g)
    else if (g) f.group = g
    return f
  }
</script>

<script lang="ts">
  import type { Account } from './api'

  let {
    policy = $bindable(),
    user = $bindable(),
    group = $bindable(),
    users,
    groups,
    keepLabel = 'Keep source owner',
    inheritLabel = 'Inherit from destination folder',
  }: {
    policy: OwnerPolicy
    user: string
    group: string
    users: Account[]
    groups: Account[]
    keepLabel?: string
    inheritLabel?: string
  } = $props()

  const id = $props.id()
</script>

<label for="{id}-policy">Ownership</label>
<select id="{id}-policy" bind:value={policy}>
  <option value="keep">{keepLabel}</option>
  <option value="inherit">{inheritLabel}</option>
  <option value="set">Set owner…</option>
</select>
{#if policy === 'set'}
  <div class="ids">
    <input list="{id}-users" bind:value={user} placeholder="user or UID" autocomplete="off" aria-label="Owner" />
    <span>:</span>
    <input list="{id}-groups" bind:value={group} placeholder="group or GID" autocomplete="off" aria-label="Group" />
  </div>
  <datalist id="{id}-users">
    {#each users as u}<option value={u.name}>{u.id}</option>{/each}
  </datalist>
  <datalist id="{id}-groups">
    {#each groups as g}<option value={g.name}>{g.id}</option>{/each}
  </datalist>
{/if}

<style>
  .ids { display: flex; gap: 6px; align-items: center; margin-top: 6px; }
  .ids input { flex: 1; min-width: 0; }
</style>
