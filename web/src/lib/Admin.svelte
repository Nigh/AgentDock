<script>
  import { api } from './api.js';

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
    if (!confirm(`Delete user "${u.username}" (#${u.uid})? Their nodes and grants are removed too.`)) return;
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
    <button onclick={() => (location.hash = '')} class="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
      ← Back
    </button>
    <h1 class="text-xl font-bold text-white">Admin</h1>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-red-950/60 px-3 py-2 text-sm text-red-400" role="alert">
      {error}
      <button class="ml-2 underline" onclick={() => (error = '')}>dismiss</button>
    </div>
  {/if}

  <!-- Users -->
  <section class="mb-6">
    <h2 class="mb-3 text-sm font-medium uppercase tracking-wider text-zinc-500">Users</h2>
    <div class="space-y-2">
      {#each users as u (u.uid)}
        <div class="flex items-center gap-3 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="font-medium text-white">{u.username}</span>
              <span class="text-xs text-zinc-500">#{u.uid}</span>
              <span class="rounded-full px-2 py-0.5 text-xs {u.role === 'admin' ? 'bg-sky-950 text-sky-400' : 'bg-zinc-800 text-zinc-400'}">{u.role}</span>
              <span class="rounded-full px-2 py-0.5 text-xs {u.status === 'active' ? 'bg-emerald-950 text-emerald-400' : 'bg-amber-950 text-amber-400'}">{u.status}</span>
            </div>
            <div class="mt-0.5 text-xs text-zinc-600">registered {fmtTime(u.created_at)}</div>
          </div>
          {#if u.status === 'pending'}
            <button onclick={() => approve(u.uid)}
              class="rounded-lg bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500">
              Approve
            </button>
          {/if}
          <button onclick={() => remove(u)} aria-label="Delete user {u.username}"
            class="rounded-lg border border-zinc-800 px-3 py-1.5 text-sm text-red-400 hover:bg-red-950/40">
            Delete
          </button>
        </div>
      {/each}
    </div>
  </section>

  <!-- Grant node access -->
  <section>
    <h2 class="mb-3 text-sm font-medium uppercase tracking-wider text-zinc-500">Grant node access</h2>
    {#if nodes.length === 0}
      <div class="rounded-xl border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-600">No nodes registered yet</div>
    {:else}
      <form onsubmit={grant} class="flex flex-wrap items-center gap-2 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
        <select bind:value={grantNode} aria-label="Node"
          class="rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white">
          {#each nodes as n (n.id)}<option value={n.id}>{n.name} (owner {n.owner})</option>{/each}
        </select>
        <span class="text-sm text-zinc-500">→</span>
        <select bind:value={grantUid} aria-label="User"
          class="rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white">
          {#each users.filter((x) => x.status === 'active') as x (x.uid)}<option value={x.uid}>{x.username} #{x.uid}</option>{/each}
        </select>
        <button class="rounded-lg bg-emerald-600 px-4 py-2 text-sm font-medium text-white hover:bg-emerald-500">Grant</button>
      </form>
    {/if}
    <p class="mt-2 text-xs text-zinc-600">Revoke from the node cards on the dashboard.</p>
  </section>
</div>
