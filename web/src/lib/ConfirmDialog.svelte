<script>
  import { dialog } from './confirm.svelte.js';

  let el;

  $effect(() => {
    if (dialog.open && !el.open) el.showModal();
  });

  function settle(ok) {
    const resolve = dialog.resolve;
    dialog.open = false;
    dialog.resolve = null;
    el.close();
    resolve?.(ok);
  }
</script>

<!-- native <dialog>: focus trap, Esc-to-cancel and ::backdrop for free -->
<dialog
  bind:this={el}
  onclose={() => dialog.open && settle(false)}
  onclick={(e) => e.target === el && settle(false)}
  class="m-auto w-full max-w-sm rounded-2xl border border-base-300/40 bg-base-100 p-0 text-base-content shadow-2xl backdrop:bg-black/60 backdrop:backdrop-blur-sm"
>
  <div class="p-6">
    <p class="text-sm leading-relaxed">{dialog.message}</p>
    <div class="mt-6 flex justify-end gap-2">
      <button
        onclick={() => settle(false)}
        class="rounded-lg border border-base-300/70 px-4 py-1.5 text-sm text-base-content/80 hover:bg-base-300/30"
      >
        Cancel
      </button>
      <button
        onclick={() => settle(true)}
        class="rounded-lg bg-error px-4 py-1.5 text-sm font-medium text-error-content hover:bg-error/80"
      >
        {dialog.confirmLabel}
      </button>
    </div>
  </div>
</dialog>
