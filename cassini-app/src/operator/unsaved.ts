import { get, writable } from "svelte/store";

// Whether a settings page holds edits its Save has not written. Set by the page
// that owns the edits; read by everything that can take the operator away from
// that page (the section nav, the Browse/Operator tabs, the back button).
export const unsavedChanges = writable(false);

// The navigation waiting on an answer, or null when nothing is being asked.
export const leavePrompt = writable<(() => void) | null>(null);

export function guardLeave(leave: () => void): void {
  if (get(unsavedChanges)) {
    leavePrompt.set(leave);
    return;
  }
  leave();
}

export function confirmLeave(): void {
  const leave = get(leavePrompt);
  leavePrompt.set(null);
  unsavedChanges.set(false);
  leave?.();
}

export function cancelLeave(): void {
  leavePrompt.set(null);
}
