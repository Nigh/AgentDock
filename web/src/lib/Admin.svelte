<script>
  import { api } from './api.js';
  import { confirmDialog } from './confirm.svelte.js';

  let users = $state([]);
  let nodes = $state([]);
  let error = $state('');

  // grant-access form
  let grantNode = $state(0);
  let grantUid = $state(0);

  async function refresh() {
    try {
      const [u, s] = await Promise.all([api.listUsers(), api.state()]);
      users = u;
      nodes = s.nodes; // admin sees all nodes
      if (!nodes.some((n) => n.id === grantNode)) grantNode = nodes[0]?.id || 0;
      if (!users.some((x) => x.uid === grantUid)) grantUid = users[0]?.uid || 0;
    } catch (e) {
      if (e.status === 401) location.reload();
      else error = e.message;
    }
  }

  $effect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  });

  async function approve(uid) {
    await api.approveUser(uid).catch((e) => (error = e.message));
    refresh();
  }

  async function remove(u) {
    if (!(await confirmDialog(`Delete user "${u.username}" (#${u.uid})? Their nodes and grants are removed too.`, 'Delete'))) return;
    await api.deleteUser(u.uid).catch((e) => (error = e.message));
    refresh();
  }

  async function grant(e) {
    e.preventDefault();
    error = '';
    try {
      await api.shareNode(grantNode, grantUid);
      refresh();
    } catch (e) {
      error = e.message;
    }
  }

  function fmtTime(t) {
    return new Date(t).toLocaleString();
  }
</script>

<div class="mx-auto max-w-3xl p-4 pb-16">
  <header class="mb-6 flex items-center gap-3">
    <button onclick={() => (location.hash = '')} class="rounded-lg border border-base-300 px-3 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30">
      ← Back
    </button>
    <h1 class="text-xl font-bold text-base-content">Admin</h1>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-error/15 px-3 py-2 text-sm text-error" role="alert">
      {error}
      <button class="ml-2 underline" onclick={() => (error = '')}>dismiss</button>
    </div>
  {/if}

  <!-- Users -->
  <section class="mb-6">
    <h2 class="mb-3 text-sm font-medium uppercase tracking-wider text-base-content/50">Users</h2>
    <div class="space-y-2">
      {#each users as u (u.uid)}
        <div class="flex items-center gap-3 rounded-xl border border-base-300/50 bg-base-100/60 p-4">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="font-medium text-base-content">{u.username}</span>
              <span class="text-xs text-base-content/50">#{u.uid}</span>
              <span class="rounded-full px-2 py-0.5 text-xs {u.role === 'admin' ? 'bg-secondary/15 text-secondary' : 'bg-base-300/30 text-base-content/70'}">{u.role}</span>
              <span class="rounded-full px-2 py-0.5 text-xs {u.status === 'active' ? 'bg-success/15 text-success' : 'bg-warning/15 text-warning'}">{u.status}</span>
            </div>
            <div class="mt-0.5 text-xs text-base-content/40">registered {fmtTime(u.created_at)}</div>
          </div>
          {#if u.status === 'pending'}
            <button onclick={() => approve(u.uid)}
              class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-content hover:bg-primary/80">
              Approve
            </button>
          {/if}
          <button onclick={() => remove(u)} aria-label="Delete user {u.username}"
            class="rounded-lg border border-base-300/50 px-3 py-1.5 text-sm text-error hover:bg-error/10">
            Delete
          </button>
        </div>
      {/each}
    </div>
  </section>

  <!-- Grant node access -->
  <section>
    <h2 class="mb-3 text-sm font-medium uppercase tracking-wider text-base-content/50">Grant node access</h2>
    {#if nodes.length === 0}
      <div class="rounded-xl border border-dashed border-base-300/50 p-6 text-center text-sm text-base-content/40">No nodes registered yet</div>
    {:else}
      <form onsubmit={grant} class="flex flex-wrap items-center gap-2 rounded-xl border border-base-300/50 bg-base-100/60 p-4">
        <select bind:value={grantNode} aria-label="Node"
          class="rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content">
          {#each nodes as n (n.id)}<option value={n.id}>{n.name} (owner {n.owner})</option>{/each}
        </select>
        <span class="text-sm text-base-content/50">→</span>
        <select bind:value={grantUid} aria-label="User"
          class="rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content">
          {#each users.filter((x) => x.status === 'active') as x (x.uid)}<option value={x.uid}>{x.username} #{x.uid}</option>{/each}
        </select>
        <button class="rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-content hover:bg-primary/80">Grant</button>
      </form>
    {/if}
    <p class="mt-2 text-xs text-base-content/40">Revoke from the node cards on the dashboard.</p>
  </section>
</div>
