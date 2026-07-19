<script>
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import { WebglAddon } from '@xterm/addon-webgl';
  import { api, terminalWsUrl } from './api.js';

  let { sessionId, sessionName } = $props();
  let spawning = $state(false);

  let container;
  let status = $state('connecting'); // connecting | connected | closed | exited
  let term, ws, fit;
  let retryTimer = null;
  let closedByUser = false;

  function connect() {
    status = 'connecting';
    ws = new WebSocket(terminalWsUrl(sessionId));
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      status = 'connected';
      sendResize();
    };

    ws.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) {
        term.write(new Uint8Array(ev.data));
        return;
      }
      try {
        const msg = JSON.parse(ev.data);
        if (msg.type === 'exited') {
          status = 'exited';
          term.write('\r\n\x1b[33m[session exited]\x1b[0m\r\n');
        } else if (msg.type === 'node_offline') {
          term.write('\r\n\x1b[31m[PC node went offline]\x1b[0m\r\n');
        } else if (msg.type === 'error') {
          term.write(`\r\n\x1b[31m[${msg.error}]\x1b[0m\r\n`);
        }
      } catch {}
    };

    ws.onclose = () => {
      if (status === 'exited' || closedByUser) return;
      status = 'closed';
      // auto-reconnect: the session survives on the PC, just re-attach
      retryTimer = setTimeout(connect, 2000);
    };
  }

  function sendResize() {
    if (ws?.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    }
  }

  $effect(() => {
    term = new Terminal({
      cursorBlink: true,
      fontSize: 14,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      scrollback: 5000,
      // xianii theme tokens as hex (xterm can't parse oklch):
      // base-200, base-content, primary, base-300
      theme: {
        background: '#161616',
        foreground: '#f2f2f2',
        cursor: '#ffa1ad',
        selectionBackground: '#404040',
      },
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(container);
    // WebGL renderer: much faster than the default DOM renderer,
    // especially on phones. On failure/context loss fall back to DOM.
    try {
      const webgl = new WebglAddon();
      webgl.onContextLoss(() => webgl.dispose());
      term.loadAddon(webgl);
    } catch {
      /* WebGL unavailable; DOM renderer works, just slower */
    }
    fit.fit();
    term.focus();

    term.onData((data) => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(data));
    });

    const ro = new ResizeObserver(() => {
      fit.fit();
      sendResize();
    });
    ro.observe(container);

    connect();

    return () => {
      closedByUser = true;
      clearTimeout(retryTimer);
      ro.disconnect();
      ws?.close();
      // The WebGL addon can throw on dispose; an uncaught error here
      // breaks Svelte's effect flush and leaves the next view dead.
      try {
        term.dispose();
      } catch {}
    };
  });

  function back() {
    location.hash = '';
  }

  // spawn a sibling session in this shell's *live* working directory
  async function newCliHere() {
    spawning = true;
    try {
      const name = (sessionName || 'cli') + '-2';
      const r = await api.createSession({ name, fromSession: sessionId });
      location.hash = `#/session/${r.id}?name=${encodeURIComponent(name)}`;
    } catch (e) {
      term.write(`\r\n\x1b[31m[new CLI failed: ${e.message}]\x1b[0m\r\n`);
    } finally {
      spawning = false;
    }
  }

  async function paste() {
    try {
      const text = await navigator.clipboard.readText();
      if (text && ws?.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(text));
    } catch {
      /* clipboard permission denied; user can long-press paste instead */
    }
  }
</script>

<div class="flex h-full flex-col">
  <header class="flex items-center gap-3 border-b border-base-300/50 bg-base-100/80 px-3 py-2">
    <button onclick={back} class="rounded-lg border border-base-300 px-3 py-1 text-sm text-base-content/80 hover:bg-base-300/30">
      ← Back
    </button>
    <div class="min-w-0 flex-1 truncate text-sm font-medium text-base-content">{sessionName || sessionId.slice(0, 8)}</div>
    <button onclick={newCliHere} disabled={spawning} title="Open a new CLI in this shell's current directory"
      class="rounded-lg border border-base-300 px-3 py-1 text-sm text-base-content/80 hover:bg-base-300/30 disabled:opacity-50">
      {spawning ? '…' : '+ CLI here'}
    </button>
    <button onclick={paste} class="rounded-lg border border-base-300 px-3 py-1 text-sm text-base-content/80 hover:bg-base-300/30">
      Paste
    </button>
    <span class="flex items-center gap-1.5 text-xs
      {status === 'connected' ? 'text-success' : status === 'exited' ? 'text-base-content/50' : 'text-warning'}">
      <span class="h-2 w-2 rounded-full
        {status === 'connected' ? 'bg-success' : status === 'exited' ? 'bg-base-300' : 'bg-warning animate-pulse'}"></span>
      {status}
    </span>
  </header>
  <div bind:this={container} class="term-wrap min-h-0 flex-1"></div>
</div>
