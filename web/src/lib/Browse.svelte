<script>
  import { api } from './api.js';

  // path lives in the hash (#/browse?path=...) so back/forward walk the tree
  let { path = '' } = $props();

  let current = $state('');
  let dirs = $state([]);
  let loading = $state(true);
  let error = $state('');
  let showHidden = $state(false);
  let opening = $state(false);

  $effect(() => {
    loading = true;
    error = '';
    api
      .browse(path)
      .then((r) => {
        current = r.path;
        dirs = r.dirs;
      })
      .catch((e) => (error = e.message))
      .finally(() => (loading = false));
  });

  const visible = $derived(showHidden ? dirs : dirs.filter((d) => !d.startsWith('.')));
  // breadcrumb: [['/', '/'], ['home', '/home'], ...]
  const crumbs = $derived(
    current
      .split('/')
      .filter(Boolean)
      .map((seg, i, all) => [seg, '/' + all.slice(0, i + 1).join('/')])
  );

  function go(p) {
    location.hash = `#/browse?path=${encodeURIComponent(p)}`;
  }

  function enter(name) {
    go(current === '/' ? '/' + name : current + '/' + name);
  }

  async function openHere() {
    opening = true;
    error = '';
    try {
      const name = (current.split('/').filter(Boolean).pop() || 'home')
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, '-');
      const r = await api.createSession(name, current, '');
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(name)}`;
    } catch (e) {
      error = e.message;
      opening = false;
    }
  }
</script>

<div class="mx-auto max-w-3xl p-4 pb-16">
  <header class="mb-4 flex items-center gap-3">
    <button onclick={() => (location.hash = '')} class="rounded-lg border border-zinc-700 px-3 py-1.5 text-sm text-zinc-300 hover:bg-zinc-800">
      ← Back
    </button>
    <h1 class="min-w-0 flex-1 truncate text-lg font-bold text-white">Browse</h1>
    <button
      onclick={openHere}
      disabled={opening || loading || !current}
      class="rounded-lg bg-emerald-600 px-4 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
    >
      {opening ? 'Opening…' : 'Open CLI here'}
    </button>
  </header>

  {#if error}
    <div class="mb-4 rounded-lg bg-red-950/60 px-3 py-2 text-sm text-red-400" role="alert">{error}</div>
  {/if}

  <!-- breadcrumb -->
  <nav class="mb-4 flex flex-wrap items-center gap-1 rounded-xl border border-zinc-800 bg-zinc-900/60 px-3 py-2 text-sm" aria-label="Path">
    <button onclick={() => go('/')} class="rounded px-1.5 py-0.5 font-mono text-zinc-400 hover:bg-zinc-800 hover:text-white">/</button>
    {#each crumbs as [seg, p], i (p)}
      {#if i > 0}<span class="text-zinc-700">/</span>{/if}
      <button onclick={() => go(p)} class="rounded px-1.5 py-0.5 font-mono {i === crumbs.length - 1 ? 'text-white' : 'text-zinc-400 hover:bg-zinc-800 hover:text-white'}">
        {seg}
      </button>
    {/each}
  </nav>

  <label class="mb-3 flex items-center gap-2 text-sm text-zinc-500">
    <input type="checkbox" bind:checked={showHidden} class="accent-emerald-600" />
    Show hidden
  </label>

  {#if loading}
    <div class="rounded-xl border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-600">Loading…</div>
  {:else}
    <div class="space-y-1">
      {#if current !== '/'}
        <button onclick={() => go(current.replace(/\/[^/]+$/, '') || '/')}
          class="flex w-full items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-3 text-left text-sm text-zinc-400 hover:bg-zinc-800">
          <span aria-hidden="true">↩</span> ..
        </button>
      {/if}
      {#each visible as d (d)}
        <button onclick={() => enter(d)}
          class="flex w-full items-center gap-3 rounded-lg border border-zinc-800 bg-zinc-900/60 px-4 py-3 text-left text-sm text-white hover:bg-zinc-800">
          <span aria-hidden="true" class="text-zinc-500">▸</span>
          <span class="truncate">{d}</span>
        </button>
      {/each}
      {#if visible.length === 0}
        <div class="rounded-xl border border-dashed border-zinc-800 p-6 text-center text-sm text-zinc-600">No subdirectories</div>
      {/if}
    </div>
  {/if}
</div>
