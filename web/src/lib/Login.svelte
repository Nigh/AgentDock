<script>
  import { api } from './api.js';

  let { onlogin } = $props();
  let username = $state('');
  let password = $state('');
  let error = $state('');
  let busy = $state(false);

  async function submit(e) {
    e.preventDefault();
    error = '';
    busy = true;
    try {
      const r = await api.login(username, password);
      onlogin(r.username);
    } catch (err) {
      error = err.message === 'unauthorized' ? 'Invalid username or password' : err.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex min-h-full items-center justify-center p-4">
  <form onsubmit={submit} class="w-full max-w-sm rounded-2xl border border-zinc-800 bg-zinc-900/60 p-8 shadow-xl">
    <div class="mb-8 text-center">
      <div class="text-2xl font-bold tracking-tight text-white">AgentDock</div>
      <div class="mt-1 text-sm text-zinc-500">Remote terminal control</div>
    </div>

    <label class="mb-4 block">
      <span class="mb-1 block text-sm text-zinc-400">Username</span>
      <input
        bind:value={username}
        autocomplete="username"
        required
        class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-white outline-none focus:border-emerald-500"
      />
    </label>

    <label class="mb-6 block">
      <span class="mb-1 block text-sm text-zinc-400">Password</span>
      <input
        type="password"
        bind:value={password}
        autocomplete="current-password"
        required
        class="w-full rounded-lg border border-zinc-700 bg-zinc-800 px-3 py-2 text-white outline-none focus:border-emerald-500"
      />
    </label>

    {#if error}
      <div class="mb-4 rounded-lg bg-red-950/60 px-3 py-2 text-sm text-red-400" role="alert">{error}</div>
    {/if}

    <button
      disabled={busy}
      class="w-full rounded-lg bg-emerald-600 py-2.5 font-medium text-white transition hover:bg-emerald-500 disabled:opacity-50"
    >
      {busy ? 'Signing in…' : 'Sign in'}
    </button>
  </form>
</div>
