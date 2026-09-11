import type { StorageArchiveFacts, StorageTransitionPreview } from "./types";

// What is in a recordings root, and what a switch would do to it, in an
// administrator's words.
//
// This file used to hold the setup wizard's decisions as well (D-708). The
// wizard is gone (D-756): a fresh install resolves its own mode and records,
// so there is no question to step anybody through, and nothing decides whether
// to ask. What is left is the two descriptions StoragePanel.svelte still
// renders, and they stay here for the reason they were here in the first
// place: `.svelte` files in this repo are tested by reading their source text,
// which can say nothing about what a sentence claims. Both go with the panel
// when phase 3 retires it.

// describeArchive says what is in a root, and refuses to call an unread one
// empty.
//
// "We could not look" and "there is nothing there" are the same zero, and every
// version of this feature that conflated them produced a screen that told an
// administrator their recordings did not exist.
export function describeArchive(archive: StorageArchiveFacts): string {
  if (!archive.probed) {
    return "Cassini could not read this folder, so it cannot say what is in it";
  }
  if (!archive.present) {
    return "not created yet — Cassini makes it when it needs it";
  }
  if (archive.meetings === 0) {
    return "empty";
  }
  return archive.meetings === 1 ? "1 recording" : `${archive.meetings} recordings`;
}

// migrationFacts is the fixed overwrite migration in administrator-facing
// language. The operator supplies every count and enforces confirmation again
// under its own lock.
export function migrationFacts(preview: StorageTransitionPreview | null): string[] {
  if (!preview) {
    return [];
  }
  if (preview.adopting_destination) {
    return ["The existing archive will be kept. Nothing will be overwritten, copied, or removed."];
  }
  const out: string[] = [];
  if (preview.overwrite_required) {
    out.push(
      `${plural(preview.overwrite_names.length, "destination artefact")} will be overwritten or removed.`,
    );
  }
  if (preview.meetings > 0) out.push(`${plural(preview.meetings, "recording")} will be copied across.`);
  if (out.length === 0) {
    out.push("Nothing moves. Only the storage mode changes.");
  }
  return out;
}

function plural(count: number, noun: string): string {
  return count === 1 ? `1 ${noun}` : `${count} ${noun}s`;
}
