// Copying a value the administrator must keep, such as a generated password
// or signing secret.
//
// The clipboard is not a given here. The app runs inside a shadow root on
// AppAPI's embedded page, where `navigator.clipboard` needs both a secure
// context and `clipboard-write` in the frame's permissions policy. Neither is
// guaranteed, so there are three layers: the async API, `execCommand` over a
// selection of the readonly field that shows the value, and — always — that
// field left selected for the keyboard. A copy button that silently does
// nothing is worse than no button.
//
// Returns whether the value reached the clipboard. On false the field is
// selected and the caller says so. Pass the field before any await: an event's
// currentTarget is gone by then.
export async function copyWithFallback(value: string, field: HTMLInputElement | null): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    // Fall through to the selection-based path.
  }
  try {
    field?.select();
    field?.setSelectionRange(0, value.length);
    if (document.execCommand("copy")) {
      return true;
    }
  } catch {
    // Nothing left to try.
  }
  field?.select();
  return false;
}
