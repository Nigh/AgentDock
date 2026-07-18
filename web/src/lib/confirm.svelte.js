// Promise-based replacement for window.confirm; rendered by ConfirmDialog.svelte
// (mounted once in App.svelte).
export const dialog = $state({
  open: false,
  message: '',
  confirmLabel: 'Confirm',
  resolve: null,
});

export function confirmDialog(message, confirmLabel = 'Confirm') {
  return new Promise((resolve) => {
    // ponytail: no queue — a second confirm while one is open resolves the
    // first as cancelled. Fine for a UI where confirms are user-initiated.
    dialog.resolve?.(false);
    Object.assign(dialog, { open: true, message, confirmLabel, resolve });
  });
}
