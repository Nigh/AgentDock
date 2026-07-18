<script>
  import { api } from './lib/api.js';
  import Login from './lib/Login.svelte';
  import Dashboard from './lib/Dashboard.svelte';
  import Terminal from './lib/Terminal.svelte';
  import Browse from './lib/Browse.svelte';
  import Admin from './lib/Admin.svelte';

  let user = $state(null); // { username, uid, role }
  let checking = $state(true);
  // hash routing: '' = dashboard, '#/session/<id>?name=x' = terminal,
  // '#/browse?node=<id>&path=...' = directory browser, '#/admin' = admin
  let route = $state(location.hash);

  window.addEventListener('hashchange', () => (route = location.hash));

  $effect(() => {
    api.me()
      .then((r) => (user = r))
      .catch(() => (user = null))
      .finally(() => (checking = false));
  });

  const sessionMatch = $derived(route.match(/^#\/session\/([a-f0-9]+)(?:\?name=(.*))?$/));
  const browseMatch = $derived(route.match(/^#\/browse\?node=(\d+)(?:&path=(.*))?$/));

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
  {#key sessionMatch[1]}
    <Terminal sessionId={sessionMatch[1]} sessionName={decodeURIComponent(sessionMatch[2] || '')} />
  {/key}
{:else if browseMatch}
  <Browse nodeId={Number(browseMatch[1])} path={decodeURIComponent(browseMatch[2] || '')} />
{:else if route === '#/admin' && user.role === 'admin'}
  <Admin />
{:else}
  <Dashboard {user} {onLogout} />
{/if}
