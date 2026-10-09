import type { RetentionSettings } from "./retention";

// No cached policy: the server owns persistence and each editor owns its draft.
// Only successful reads/writes publish snapshots to the shell's reminder.
const listeners = new Set<(settings: RetentionSettings) => void>();

export function onRetentionChanged(listener: (settings: RetentionSettings) => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function notifyRetentionChanged(settings: RetentionSettings): void {
  for (const listener of [...listeners]) {
    try { listener(settings); }
    catch (error) { console.error("Cassini: retention listener failed.", error); }
  }
}
