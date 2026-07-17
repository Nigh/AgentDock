<script>
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import { terminalWsUrl } from './api.js';

  let { sessionId, sessionName } = $props();

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
      theme: {
        background: '#0a0a0f',
        foreground: '#e4e4e7',
        cursor: '#34d399',
        selectionBackground: '#334155',
      },
    });
    fit = new FitAddon();
    term.loadAddon(fit);
    term.open(container);
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
      term.dispose();
    };
  });

  function back() {
    location.hash = '';
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
  <header class="flex items-center gap-3 border-b border-zinc-800 bg-zinc-900/80 px-3 py-2">
    <button onclick={back} class="rounded-lg border border-zinc-700 px-3 py-1 text-sm text-zinc-300 hover:bg-zinc-800">
      ← Back
    </button>
    <div class="min-w-0 flex-1 truncate text-sm font-medium text-white">{sessionName || sessionId.slice(0, 8)}</div>
    <button onclick={paste} class="rounded-lg border border-zinc-700 px-3 py-1 text-sm text-zinc-300 hover:bg-zinc-800">
      Paste
    </button>
    <span class="flex items-center gap-1.5 text-xs
      {status === 'connected' ? 'text-emerald-500' : status === 'exited' ? 'text-zinc-500' : 'text-amber-500'}">
      <span class="h-2 w-2 rounded-full
        {status === 'connected' ? 'bg-emerald-500' : status === 'exited' ? 'bg-zinc-600' : 'bg-amber-500 animate-pulse'}"></span>
      {status}
    </span>
  </header>
  <div bind:this={container} class="term-wrap min-h-0 flex-1"></div>
</div>
