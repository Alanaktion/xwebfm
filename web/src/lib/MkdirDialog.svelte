<script lang="ts">
  import { api, type Account, type OwnerPolicy } from './api'
  import { joinPath } from './format'
  import Modal from './Modal.svelte'
  import OwnerPicker, { ownerFields } from './OwnerPicker.svelte'

  let {
    dir,
    users,
    groups,
    onclose,
    ondone,
  }: {
    dir: string
    users: Account[]
    groups: Account[]
    onclose: () => void
    ondone: (msg: string) => void
  } = $props()

  let name = $state('')
  let mode = $state('0755')
  let policy = $state<OwnerPolicy>('inherit')
  let user = $state('')
  let group = $state('')
  let busy = $state(false)
  let error = $state('')

  async function submit() {
    busy = true
    error = ''
    try {
      await api.mkdir({ path: joinPath(dir, name), mode, ...ownerFields(policy, user, group) })
      ondone(`Created ${name}`)
    } catch (e) {
      error = (e as Error).message
    } finally {
      busy = false
    }
  }
</script>

<Modal title="New folder in {dir}" submitLabel="Create" {busy} {error} onsubmit={submit} {onclose}>
  <label for="name">Name</label>
  <!-- svelte-ignore a11y_autofocus -->
  <input id="name" bind:value={name} style="width: 100%" autocomplete="off" autofocus />
  <label for="mode">Mode</label>
  <input id="mode" class="mono" bind:value={mode} size="6" autocomplete="off" />
  <OwnerPicker
    bind:policy
    bind:user
    bind:group
    {users}
    {groups}
    keepLabel="Owned by root"
    inheritLabel="Inherit from parent folder"
  />
</Modal>
