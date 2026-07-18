<script>
  import { api } from './api.js';

  let { username, onLogout } = $props();

  let node = $state({ name: '', connected: false });
  let sessions = $state([]);
  let directories = $state([]);
  let error = $state('');

  // new-session form
  let showNew = $state(false);
  let newName = $state('');
  let newDir = $state('');
  let newShell = $state('');
  let creating = $state(false);

  // new-directory form
  let showNewDir = $state(false);
  let dirName = $state('');
  let dirPath = $state('');

  async function refresh() {
    try {
      const s = await api.state();
      node = s.node;
      sessions = s.sessions;
      directories = s.directories;
    } catch (e) {
      if (e.status === 401) location.reload();
    }
  }

  $effect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  });

  function openSession(s) {
    location.hash = `#/session/${s.id}?name=${encodeURIComponent(s.name)}`;
  }

  async function createSession(e) {
    e.preventDefault();
    error = '';
    creating = true;
    try {
      const r = await api.createSession(newName, newDir, newShell);
      showNew = false;
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(newName)}`;
      newName = newDir = newShell = '';
    } catch (e) {
      error = e.message;
    } finally {
      creating = false;
    }
  }

  // "quick open": one tap on a saved directory creates a session there
  async function quickOpen(dir) {
    error = '';
    try {
      const name = dir.name.toLowerCase().replace(/[^a-z0-9]+/g, '-');
      const r = await api.createSession(name, dir.path, '');
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(name)}`;
    } catch (e) {
      error = e.message;
    }
  }

  async function addDirectory(e) {
    e.preventDefault();
    error = '';
    try {
      await api.createDirectory(dirName, dirPath);
      dirName = dirPath = '';
      showNewDir = false;
      refresh();
    } catch (e) {
      error = e.message;
    }
  }

  async function removeDirectory(id) {
    await api.deleteDirectory(id).catch(() => {});
    refresh();
  }

  async function killSession(s) {
    if (!confirm(`Kill session "${s.name}"? The shell and everything in it will be terminated.`)) return;
    await api.killSession(s.id).catch((e) => (error = e.message));
    refresh();
  }

  function fmtTime(t) {
    return new Date(t).toLocaleString();
  }
</script>

<div class="mx-auto max-w-3xl p-4 pb-16">
  <header class="mb-6 flex items-center justify-between">
    <div>
      <h1 class="text-xl font-bold text-white">AgentDock</h1>
      <div class="text-sm text-zinc-500">{username}</div>
    </div>
    <button onclick={onLogout} class="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
      Sign out
    </button>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-red-950/60 px-3 py-2 text-sm text-red-400" role="alert">
      {error}
      <button class="ml-2 underline" onclick={() => (error = '')}>dismiss</button>
    </div>
  {/if}

  <!-- Node status -->
  <section class="mb-6 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
    <div class="text-xs font-medium uppercase tracking-wider text-zinc-500">Connected PC</div>
    <div class="mt-2 flex items-center gap-2">
      <span class="h-2.5 w-2.5 rounded-full {node.connected ? 'bg-emerald-500' : 'bg-red-500'}"></span>
      {#if node.connected}
        <span class="font-medium text-white">{node.name || 'unnamed'}</span>
        <span class="text-sm text-emerald-500">online</span>
      {:else}
        <span class="text-sm text-zinc-400">No PC connected — run <code class="rounded bg-zinc-800 px-1">agent-client connect</code></span>
      {/if}
    </div>
  </section>

  <!-- Sessions -->
  <section class="mb-6">
    <div class="mb-3 flex items-center justify-between">
      <h2 class="text-sm font-medium uppercase tracking-wider text-zinc-500">Sessions</h2>
      <div class="flex gap-2">
        <button
          onclick={() => (location.hash = '#/browse')}
          disabled={!node.connected}
          class="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800 disabled:opacity-40"
        >
          Browse…
        </button>
        <button
          onclick={() => (showNew = !showNew)}
          disabled={!node.connected}
          class="rounded-lg bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
        >
          New Session
        </button>
      </div>
    </div>

    {#if showNew}
      <form onsubmit={createSession} class="mb-4 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
        <div class="grid gap-3 sm:grid-cols-3">
          <label class="block">
            <span class="mb-1 block text-xs text-zinc-500">Name</span>
            <input bind:value={newName} required placeholder="cursor-robot"
              class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white outline-none focus:border-emerald-500" />
          </label>
          <label class="block">
            <span class="mb-1 block text-xs text-zinc-500">Directory (optional)</span>
            <input bind:value={newDir} placeholder="~ by default" list="dir-list"
              class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white outline-none focus:border-emerald-500" />
            <datalist id="dir-list">
              {#each directories as d}<option value={d.path}>{d.name}</option>{/each}
            </datalist>
          </label>
          <label class="block">
            <span class="mb-1 block text-xs text-zinc-500">Shell (optional)</span>
            <input bind:value={newShell} placeholder="$SHELL by default"
              class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white outline-none focus:border-emerald-500" />
          </label>
        </div>
        <div class="mt-3 flex gap-2">
          <button disabled={creating} class="rounded-lg bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50">
            {creating ? 'Creating…' : 'Create'}
          </button>
          <button type="button" onclick={() => (showNew = false)} class="rounded-lg border border-zinc-700 px-4 py-1.5 text-sm text-zinc-300">
            Cancel
          </button>
        </div>
      </form>
    {/if}

    {#if sessions.length === 0}
      <div class="rounded-xl border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-600">No sessions yet</div>
    {/if}

    <div class="space-y-2">
      {#each sessions as s (s.id)}
        <div class="flex items-center gap-3 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="truncate font-medium text-white">{s.name}</span>
              <span class="rounded-full px-2 py-0.5 text-xs {s.status === 'running' ? 'bg-emerald-950 text-emerald-400' : 'bg-zinc-800 text-zinc-500'}">
                {s.status}
              </span>
            </div>
            <div class="mt-0.5 truncate text-xs text-zinc-500">{s.cwd}</div>
            <div class="text-xs text-zinc-600">created {fmtTime(s.created_at)}</div>
          </div>
          {#if s.status === 'running'}
            <button onclick={() => openSession(s)}
              class="rounded-lg bg-zinc-800 px-4 py-2 text-sm font-medium text-white hover:bg-zinc-700">
              Open
            </button>
          {/if}
          <button onclick={() => killSession(s)} aria-label="Kill session {s.name}"
            class="rounded-lg border border-zinc-800 px-3 py-2 text-sm text-red-400 hover:bg-red-950/40">
            {s.status === 'running' ? 'Kill' : 'Remove'}
          </button>
        </div>
      {/each}
    </div>
  </section>

  <!-- Directories -->
  <section>
    <div class="mb-3 flex items-center justify-between">
      <h2 class="text-sm font-medium uppercase tracking-wider text-zinc-500">Saved Directories</h2>
      <button onclick={() => (showNewDir = !showNewDir)}
        class="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
        Add
      </button>
    </div>

    {#if showNewDir}
      <form onsubmit={addDirectory} class="mb-4 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
        <div class="grid gap-3 sm:grid-cols-2">
          <input bind:value={dirName} required placeholder="Name, e.g. Robot Controller"
            class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white outline-none focus:border-emerald-500" />
          <input bind:value={dirPath} required placeholder="/home/user/work/robot"
            class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-sm text-white outline-none focus:border-emerald-500" />
        </div>
        <div class="mt-3 flex gap-2">
          <button class="rounded-lg bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-emerald-500">Save</button>
          <button type="button" onclick={() => (showNewDir = false)} class="rounded-lg border border-zinc-700 px-4 py-1.5 text-sm text-zinc-300">Cancel</button>
        </div>
      </form>
    {/if}

    {#if directories.length === 0}
      <div class="rounded-xl border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-600">
        Save frequently used project directories for one-tap sessions
      </div>
    {/if}

    <div class="space-y-2">
      {#each directories as d (d.id)}
        <div class="flex items-center gap-3 rounded-xl border border-zinc-800 bg-zinc-900/60 p-4">
          <button onclick={() => quickOpen(d)} disabled={!node.connected}
            class="min-w-0 flex-1 text-left disabled:opacity-50">
            <div class="truncate font-medium text-white">{d.name}</div>
            <div class="truncate text-xs text-zinc-500">{d.path}</div>
          </button>
          <button onclick={() => removeDirectory(d.id)} aria-label="Delete directory {d.name}"
            class="rounded-lg border border-zinc-800 px-3 py-2 text-sm text-zinc-500 hover:text-red-400">
            ✕
          </button>
        </div>
      {/each}
    </div>
  </section>
</div>
