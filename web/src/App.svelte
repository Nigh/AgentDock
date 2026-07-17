<script>
  import { api } from './lib/api.js';
  import Login from './lib/Login.svelte';
  import Dashboard from './lib/Dashboard.svelte';
  import Terminal from './lib/Terminal.svelte';

  let user = $state(null);
  let checking = $state(true);
  // hash routing: '' = dashboard, '#/session/<id>?name=x' = terminal
  let route = $state(location.hash);

  window.addEventListener('hashchange', () => (route = location.hash));

  $effect(() => {
    api.me()
      .then((r) => (user = r.username))
      .catch(() => (user = null))
      .finally(() => (checking = false));
  });

  const sessionMatch = $derived(route.match(/^#\/session\/([a-f0-9]+)(?:\?name=(.*))?$/));

  function onLogout() {
    api.logout().finally(() => {
      user = null;
      location.hash = '';
    });
  }
</script>

{#if checking}
  <div class="flex h-full items-center justify-center text-zinc-500">Loading…</div>
{:else if !user}
  <Login onlogin={(u) => (user = u)} />
{:else if sessionMatch}
  <Terminal sessionId={sessionMatch[1]} sessionName={decodeURIComponent(sessionMatch[2] || '')} />
{:else}
  <Dashboard username={user} {onLogout} />
{/if}
