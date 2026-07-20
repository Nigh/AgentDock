<script>
  import { api } from './api.js';
  import { confirmDialog } from './confirm.svelte.js';

  let { user, onLogout } = $props();

  let nodes = $state([]);
  let sessions = $state([]);
  let directories = $state([]);
  let tokens = $state([]);
  let error = $state('');

  // node-tokens card
  let showConnect = $state(false);
  let newTokenName = $state('');
  let freshToken = $state(''); // plaintext of the last created token, shown once
  let copied = $state(false);

  // new-session form (nodeId selects which PC)
  let newForNode = $state(0); // node id the form is open for, 0 = closed
  let newName = $state('');
  let newDir = $state('');
  let newShell = $state('');
  let creating = $state(false);

  // share form
  let shareForNode = $state(0);
  let shareUid = $state('');

  // new-directory form
  let showNewDir = $state(false);
  let dirName = $state('');
  let dirPath = $state('');
  // node used by directory quick-open when several are online
  let quickNode = $state(0);

  const onlineNodes = $derived(nodes.filter((n) => n.connected));

  async function refresh() {
    try {
      const s = await api.state();
      nodes = s.nodes;
      sessions = s.sessions;
      directories = s.directories;
      tokens = s.tokens;
      if (!onlineNodes.some((n) => n.id === quickNode)) {
        quickNode = onlineNodes[0]?.id || 0;
      }
    } catch (e) {
      if (e.status === 401) location.reload();
    }
  }

  $effect(() => {
    refresh();
    const t = setInterval(refresh, 5000);
    return () => clearInterval(t);
  });

  async function createToken(e) {
    e.preventDefault();
    error = '';
    try {
      const r = await api.createNodeToken(newTokenName);
      freshToken = r.token;
      newTokenName = '';
      copied = false;
      refresh();
    } catch (e) {
      error = e.message;
    }
  }

  function tokenLabel(t) {
    return t.name || `token #${t.id}`;
  }

  // PCs that last connected with this token (owned nodes only)
  function nodesOfToken(t) {
    return nodes.filter((n) => n.token_id === t.id);
  }

  async function deleteToken(t) {
    const online = nodesOfToken(t).filter((n) => n.connected);
    const warn = online.length
      ? ` ${online.map((n) => n.name).join(', ')} will be disconnected.`
      : '';
    if (!(await confirmDialog(`Delete ${tokenLabel(t)}? PCs using it can no longer connect.${warn}`, 'Delete'))) return;
    await api.deleteNodeToken(t.id).catch((e) => (error = e.message));
    refresh();
  }

  const connectCmd = $derived(
    `agent-client connect --server ${location.origin} --token ${freshToken || '<token>'}`
  );

  function copyCmd() {
    navigator.clipboard.writeText(connectCmd).then(() => (copied = true));
  }

  function openSession(s) {
    location.hash = `#/session/${s.id}?name=${encodeURIComponent(s.name)}`;
  }

  async function createSession(e) {
    e.preventDefault();
    error = '';
    creating = true;
    try {
      const r = await api.createSession({ name: newName, nodeId: newForNode, cwd: newDir, shell: newShell });
      const name = newName;
      newForNode = 0;
      newName = newDir = newShell = '';
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(name)}`;
    } catch (e) {
      error = e.message;
    } finally {
      creating = false;
    }
  }

  // "quick open": one tap on a saved directory creates a session there
  async function quickOpen(dir) {
    error = '';
    if (!quickNode) {
      error = 'no PC online';
      return;
    }
    try {
      const name = dir.name.toLowerCase().replace(/[^a-z0-9]+/g, '-');
      const r = await api.createSession({ name, nodeId: quickNode, cwd: dir.path });
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(name)}`;
    } catch (e) {
      error = e.message;
    }
  }

  async function share(e, nodeId) {
    e.preventDefault();
    error = '';
    try {
      await api.shareNode(nodeId, Number(shareUid));
      shareForNode = 0;
      shareUid = '';
      refresh();
    } catch (e) {
      error = e.message;
    }
  }

  async function revoke(nodeId, uid) {
    await api.revokeShare(nodeId, uid).catch((e) => (error = e.message));
    refresh();
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
    if (!(await confirmDialog(`Kill session "${s.name}"? The shell and everything in it will be terminated.`, 'Kill'))) return;
    await api.killSession(s.id).catch((e) => (error = e.message));
    refresh();
  }

  // Spawn a new PTY with the exited session's name/cwd/shell (spawn-time cwd;
  // live cwd is gone with the process), then drop the exited card.
  async function reopenSession(s) {
    error = '';
    creating = true;
    try {
      const r = await api.createSession({
        name: s.name, nodeId: s.node_id, cwd: s.cwd, shell: s.shell,
      });
      await api.killSession(s.id).catch(() => {});
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(s.name)}`;
    } catch (e) {
      error = e.message;
    } finally {
      creating = false;
    }
  }

  function sessionsOf(nodeId) {
    return sessions.filter((s) => s.node_id === nodeId);
  }

  function fmtTime(t) {
    return new Date(t).toLocaleString();
  }
</script>

<div class="mx-auto max-w-3xl p-4 pb-16">
  <header class="mb-6 flex items-center justify-between">
    <div>
      <h1 class="text-xl font-bold text-base-content">AgentDock</h1>
      <div class="text-sm text-base-content/50">{user.username} <span class="text-base-content/40">#{user.uid}</span></div>
    </div>
    <div class="flex gap-2">
      {#if user.role === 'admin'}
        <button onclick={() => (location.hash = '#/admin')}
          class="rounded-lg border border-base-300 px-3 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30">
          Admin
        </button>
      {/if}
      <button onclick={onLogout} class="rounded-lg border border-base-300 px-3 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30">
        Sign out
      </button>
    </div>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-error/15 px-3 py-2 text-sm text-error" role="alert">
      {error}
      <button class="ml-2 underline" onclick={() => (error = '')}>dismiss</button>
    </div>
  {/if}

  <!-- Node tokens -->
  <section class="mb-6 rounded-xl border border-base-300/50 bg-base-100/60 p-4">
    <div class="flex items-center justify-between">
      <div class="text-xs font-medium uppercase tracking-wider text-base-content/50">
        Node Tokens {#if tokens.length}<span class="normal-case tracking-normal">({tokens.length}/16)</span>{/if}
      </div>
      <button onclick={() => (showConnect = !showConnect)} class="text-sm text-base-content/70 hover:text-base-content">
        {showConnect ? 'Hide' : 'Show'}
      </button>
    </div>
    {#if showConnect}
      <p class="mt-2 text-sm text-base-content/70">
        Each PC connects with a node token; a node belongs to your account automatically. Create one
        token per PC so you can revoke them independently (up to 16).
      </p>

      {#if tokens.length}
        <div class="mt-3 space-y-2">
          {#each tokens as t (t.id)}
            <div class="flex flex-wrap items-center gap-2 rounded-lg border border-base-300/50 bg-base-200/40 px-3 py-2">
              <span class="text-sm font-medium text-base-content">{tokenLabel(t)}</span>
              {#each nodesOfToken(t) as n (n.id)}
                <span class="flex items-center gap-1 rounded-full bg-base-300/30 px-2 py-0.5 text-xs text-base-content/80">
                  <span class="h-1.5 w-1.5 rounded-full {n.connected ? 'bg-success' : 'bg-error'}"></span>{n.name}
                </span>
              {:else}
                <span class="text-xs text-base-content/40">no PC yet</span>
              {/each}
              <button onclick={() => deleteToken(t)} aria-label="Delete token {tokenLabel(t)}"
                class="ml-auto rounded-lg border border-base-300/50 px-2 py-1 text-xs text-base-content/50 hover:text-error">
                ✕
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <form onsubmit={createToken} class="mt-3 flex flex-wrap items-center gap-2">
        <input bind:value={newTokenName} placeholder="alias, e.g. work-laptop" maxlength="64"
          class="w-48 rounded-lg border border-base-300 bg-base-300/30 px-3 py-1.5 text-sm text-base-content outline-none focus:border-primary" />
        <button disabled={tokens.length >= 16}
          class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-content hover:bg-primary/80 disabled:opacity-40">
          New token
        </button>
        {#if freshToken}
          <button type="button" onclick={copyCmd} class="rounded-lg border border-base-300 px-3 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30">
            {copied ? 'Copied' : 'Copy command'}
          </button>
        {/if}
      </form>
      {#if freshToken}
        <code class="mt-3 block overflow-x-auto rounded-lg bg-base-200 px-3 py-2 text-xs text-primary">{connectCmd}</code>
        <p class="mt-1 text-xs text-warning">Shown once — the server only stores a hash.</p>
      {/if}
    {/if}
  </section>

  <!-- Nodes -->
  <section class="mb-6">
    <h2 class="mb-3 text-sm font-medium uppercase tracking-wider text-base-content/50">PCs</h2>

    {#if nodes.length === 0}
      <div class="rounded-xl border border-dashed border-base-300/50 p-6 text-center text-sm text-base-content/40">
        No PC yet — generate a token above and run agent-client
      </div>
    {/if}

    <div class="space-y-3">
      {#each nodes as n (n.id)}
        <div class="rounded-xl border border-base-300/50 bg-base-100/60 p-4">
          <div class="flex flex-wrap items-center gap-2">
            <span class="h-2.5 w-2.5 rounded-full {n.connected ? 'bg-success' : 'bg-error'}"></span>
            <span class="font-medium text-base-content">{n.name}</span>
            <span class="text-xs text-base-content/50">owner {n.owner} #{n.owner_uid}</span>
            {#if n.token_name || n.token_id}
              <span class="rounded-full bg-base-300/30 px-2 py-0.5 text-xs text-base-content/60" title="node token">
                {n.token_name || `token #${n.token_id}`}
              </span>
            {/if}
            <div class="ml-auto flex gap-2">
              <button onclick={() => (shareForNode = shareForNode === n.id ? 0 : n.id)}
                class="rounded-lg border border-base-300 px-3 py-1 text-sm text-base-content/80 hover:bg-base-300/30">
                Share
              </button>
              <button onclick={() => (location.hash = `#/browse?node=${n.id}`)} disabled={!n.connected}
                class="rounded-lg border border-base-300 px-3 py-1 text-sm text-base-content/80 hover:bg-base-300/30 disabled:opacity-40">
                Browse…
              </button>
              <button onclick={() => (newForNode = newForNode === n.id ? 0 : n.id)} disabled={!n.connected}
                class="rounded-lg bg-primary px-3 py-1 text-sm font-medium text-primary-content hover:bg-primary/80 disabled:opacity-40">
                New Session
              </button>
            </div>
          </div>

          {#if n.shares?.length}
            <div class="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-base-content/50">
              shared with
              {#each n.shares as sh (sh.uid)}
                <span class="flex items-center gap-1 rounded-full bg-base-300/30 px-2 py-0.5 text-base-content/80">
                  {sh.username} #{sh.uid}
                  <button onclick={() => revoke(n.id, sh.uid)} aria-label="Revoke access for {sh.username}"
                    class="text-base-content/50 hover:text-error">✕</button>
                </span>
              {/each}
            </div>
          {/if}

          {#if shareForNode === n.id}
            <form onsubmit={(e) => share(e, n.id)} class="mt-3 flex gap-2">
              <input bind:value={shareUid} required placeholder="uid, e.g. 2" inputmode="numeric" pattern="[0-9]+"
                class="w-32 rounded-lg border border-base-300 bg-base-300/30 px-3 py-1.5 text-sm text-base-content outline-none focus:border-primary" />
              <button class="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-content hover:bg-primary/80">Grant</button>
            </form>
          {/if}

          {#if newForNode === n.id}
            <form onsubmit={createSession} class="mt-3 rounded-lg border border-base-300/50 bg-base-200/60 p-3">
              <div class="grid gap-3 sm:grid-cols-3">
                <label class="block">
                  <span class="mb-1 block text-xs text-base-content/50">Name</span>
                  <input bind:value={newName} required placeholder="cursor-robot"
                    class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content outline-none focus:border-primary" />
                </label>
                <label class="block">
                  <span class="mb-1 block text-xs text-base-content/50">Directory (optional)</span>
                  <input bind:value={newDir} placeholder="~ by default" list="dir-list"
                    class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content outline-none focus:border-primary" />
                  <datalist id="dir-list">
                    {#each directories as d}<option value={d.path}>{d.name}</option>{/each}
                  </datalist>
                </label>
                <label class="block">
                  <span class="mb-1 block text-xs text-base-content/50">Shell (optional)</span>
                  <input bind:value={newShell} placeholder="$SHELL by default"
                    class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content outline-none focus:border-primary" />
                </label>
              </div>
              <div class="mt-3 flex gap-2">
                <button disabled={creating} class="rounded-lg bg-primary px-4 py-1.5 text-sm font-medium text-primary-content hover:bg-primary/80 disabled:opacity-50">
                  {creating ? 'Creating…' : 'Create'}
                </button>
                <button type="button" onclick={() => (newForNode = 0)} class="rounded-lg border border-base-300 px-4 py-1.5 text-sm text-base-content/80">
                  Cancel
                </button>
              </div>
            </form>
          {/if}

          <!-- sessions on this node -->
          <div class="mt-3 space-y-2">
            {#each sessionsOf(n.id) as s (s.id)}
              <div class="flex items-center gap-3 rounded-lg border border-base-300/50 bg-base-200/40 p-3">
                <div class="min-w-0 flex-1">
                  <div class="flex items-center gap-2">
                    <span class="truncate font-medium text-base-content">{s.name}</span>
                    <span class="rounded-full px-2 py-0.5 text-xs {s.status === 'running' ? 'bg-success/15 text-success' : 'bg-base-300/30 text-base-content/50'}">
                      {s.status}
                    </span>
                  </div>
                  <div class="mt-0.5 truncate text-xs text-base-content/50">{s.cwd}</div>
                  <div class="text-xs text-base-content/40">created {fmtTime(s.created_at)}</div>
                </div>
                {#if s.status === 'running'}
                  <button onclick={() => openSession(s)}
                    class="rounded-lg bg-base-300/30 px-4 py-2 text-sm font-medium text-base-content hover:bg-base-300/50">
                    Open
                  </button>
                {:else if n.connected}
                  <button onclick={() => reopenSession(s)} disabled={creating}
                    class="rounded-lg bg-base-300/30 px-4 py-2 text-sm font-medium text-base-content hover:bg-base-300/50 disabled:opacity-50">
                    Reopen
                  </button>
                {/if}
                <button onclick={() => killSession(s)} aria-label="Kill session {s.name}"
                  class="rounded-lg border border-base-300/50 px-3 py-2 text-sm text-error hover:bg-error/10">
                  {s.status === 'running' ? 'Kill' : 'Remove'}
                </button>
              </div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
  </section>

  <!-- Directories -->
  <section>
    <div class="mb-3 flex items-center justify-between gap-2">
      <h2 class="text-sm font-medium uppercase tracking-wider text-base-content/50">Saved Directories</h2>
      <div class="flex items-center gap-2">
        {#if onlineNodes.length > 1}
          <select bind:value={quickNode} aria-label="PC for quick open"
            class="rounded-lg border border-base-300 bg-base-300/30 px-2 py-1.5 text-sm text-base-content/80">
            {#each onlineNodes as n (n.id)}<option value={n.id}>{n.name}</option>{/each}
          </select>
        {/if}
        <button onclick={() => (showNewDir = !showNewDir)}
          class="rounded-lg border border-base-300 px-3 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30">
          Add
        </button>
      </div>
    </div>

    {#if showNewDir}
      <form onsubmit={addDirectory} class="mb-4 rounded-xl border border-base-300/50 bg-base-100/60 p-4">
        <div class="grid gap-3 sm:grid-cols-2">
          <input bind:value={dirName} required placeholder="Name, e.g. Robot Controller"
            class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content outline-none focus:border-primary" />
          <input bind:value={dirPath} required placeholder="/home/user/work/robot"
            class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-sm text-base-content outline-none focus:border-primary" />
        </div>
        <div class="mt-3 flex gap-2">
          <button class="rounded-lg bg-primary px-4 py-1.5 text-sm font-medium text-primary-content hover:bg-primary/80">Save</button>
          <button type="button" onclick={() => (showNewDir = false)} class="rounded-lg border border-base-300 px-4 py-1.5 text-sm text-base-content/80">Cancel</button>
        </div>
      </form>
    {/if}

    {#if directories.length === 0}
      <div class="rounded-xl border border-dashed border-base-300/50 p-6 text-center text-sm text-base-content/40">
        Save frequently used project directories for one-tap sessions
      </div>
    {/if}

    <div class="space-y-2">
      {#each directories as d (d.id)}
        <div class="flex items-center gap-3 rounded-xl border border-base-300/50 bg-base-100/60 p-4">
          <button onclick={() => quickOpen(d)} disabled={!quickNode}
            class="min-w-0 flex-1 text-left disabled:opacity-50">
            <div class="truncate font-medium text-base-content">{d.name}</div>
            <div class="truncate text-xs text-base-content/50">{d.path}</div>
          </button>
          <button onclick={() => removeDirectory(d.id)} aria-label="Delete directory {d.name}"
            class="rounded-lg border border-base-300/50 px-3 py-2 text-sm text-base-content/50 hover:text-error">
            ✕
          </button>
        </div>
      {/each}
    </div>
  </section>
</div>
