<script>
  import { api } from './api.js';

  let { onlogin } = $props();
  let mode = $state('login'); // 'login' | 'register'
  let username = $state('');
  let password = $state('');
  let error = $state('');
  let notice = $state('');
  let busy = $state(false);

  async function submit(e) {
    e.preventDefault();
    error = notice = '';
    busy = true;
    try {
      if (mode === 'register') {
        const r = await api.register(username, password);
        if (r.status === 'pending') {
          notice = 'Account created. An admin must approve it before you can sign in.';
          mode = 'login';
          return;
        }
        // first user: active admin, sign straight in
      }
      const r = await api.login(username, password);
      onlogin(r);
    } catch (err) {
      error = err.message === 'unauthorized' ? 'Invalid username or password' : err.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="flex min-h-full items-center justify-center p-4">
  <form onsubmit={submit} class="w-full max-w-sm rounded-2xl border border-base-300/50 bg-base-100/60 p-8 shadow-xl">
    <div class="mb-8 text-center">
      <div class="text-2xl font-bold tracking-tight text-base-content">AgentDock</div>
      <div class="mt-1 text-sm text-base-content/50">Remote terminal control</div>
    </div>

    <label class="mb-4 block">
      <span class="mb-1 block text-sm text-base-content/70">Username</span>
      <input
        bind:value={username}
        autocomplete="username"
        required
        class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-base-content outline-none focus:border-primary"
      />
    </label>

    <label class="mb-6 block">
      <span class="mb-1 block text-sm text-base-content/70">Password</span>
      <input
        type="password"
        bind:value={password}
        autocomplete={mode === 'register' ? 'new-password' : 'current-password'}
        required
        minlength={mode === 'register' ? 8 : undefined}
        class="w-full rounded-lg border border-base-300 bg-base-300/30 px-3 py-2 text-base-content outline-none focus:border-primary"
      />
    </label>

    {#if error}
      <div class="mb-4 rounded-lg bg-error/15 px-3 py-2 text-sm text-error" role="alert">{error}</div>
    {/if}
    {#if notice}
      <div class="mb-4 rounded-lg bg-success/15 px-3 py-2 text-sm text-success" role="status">{notice}</div>
    {/if}

    <button
      disabled={busy}
      class="w-full rounded-lg bg-primary py-2.5 font-medium text-primary-content transition hover:bg-primary/80 disabled:opacity-50"
    >
      {busy ? '…' : mode === 'register' ? 'Create account' : 'Sign in'}
    </button>

    <button
      type="button"
      onclick={() => { mode = mode === 'login' ? 'register' : 'login'; error = notice = ''; }}
      class="mt-4 w-full text-center text-sm text-base-content/50 hover:text-base-content/80"
    >
      {mode === 'login' ? 'No account? Register' : 'Have an account? Sign in'}
    </button>
  </form>
</div>
