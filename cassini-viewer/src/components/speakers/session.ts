import { get, writable } from "svelte/store";

import {
  applyPending,
  describeSpeakerEditsError,
  emptyPending,
  speakerEditsBody,
  SpeakerEditsError,
  withoutSplit,
  withSplit,
  type PendingSpeakerEdits,
  type SpeakerEditsDoc,
  type SpeakerEditsState,
} from "../../viewer/speakerEdits";

export type LoadSpeakerEdits = () => Promise<SpeakerEditsState>;
export type SaveSpeakerEdits = (expectRevision: number, doc: SpeakerEditsDoc) => Promise<SpeakerEditsState>;

export interface SpeakersState {
  // "off" when this surface cannot change speakers; "failed" when the operator
  // would not say (an operator older than the route answers 404): both leave
  // the People panel read-only rather than showing controls that cannot work.
  status: "off" | "loading" | "ready" | "failed";
  server: SpeakerEditsState | null;
  // The speaker-edits revision the recording on screen was published with:
  // 0 when it carries none, null while none is loaded. Told by whoever loads
  // the recording (`showing`), never taken from the operator's answer — a
  // copy read earlier in the tab can be older than what the operator applied.
  shownRevision: number | null;
  // Names typed and "same person" picks, not yet saved. Kept here rather than
  // in the panel so closing the popover does not throw them away.
  pending: PendingSpeakerEdits;
  saving: boolean;
  error: string;
}

const OFF: SpeakersState = {
  status: "off",
  server: null,
  shownRevision: null,
  pending: emptyPending(),
  saving: false,
  error: "",
};

export const SPEAKER_POLL_MS = 3000;
// The longest wait between attempts to read a meeting's speaker edits after
// the first read failed.
const LOAD_RETRY_MAX_MS = 60_000;

// Whether the recording on screen is older than what the operator applied,
// so "Voices updated · Reload" is due.
//
// A recording published with edits says which revision; one without says
// nothing, and is behind only if the last apply separated some voice: an
// apply that changed nothing (undoing every split, a device where only one
// voice was found) republishes the original speakers, with no record.
export function recordingBehind({ server, shownRevision }: Pick<SpeakersState, "server" | "shownRevision">): boolean {
  if (!server || server.state !== "idle" || shownRevision === null) return false;
  if (shownRevision > 0) return server.appliedRevision > shownRevision;
  return (
    server.appliedRevision > 0 &&
    Boolean(server.report?.splits.some((split) => !split.inconclusive && split.voices.length > 1))
  );
}

// One meeting's speaker edits: the operator's state, the reader's unsaved
// edits, and polling while the operator applies a saved revision. Every write
// is the whole desired document with the revision it was made from, so there is
// no queue to reconcile: a conflict means someone else saved first, and the
// answer is to show what they saved.
export function createSpeakersSession(options: { pollMs?: number } = {}) {
  const pollMs = options.pollMs ?? SPEAKER_POLL_MS;
  const state = writable<SpeakersState>(OFF);
  let load: LoadSpeakerEdits | null = null;
  let save: SaveSpeakerEdits | null = null;
  let generation = 0;
  let pollTimer: ReturnType<typeof setTimeout> | undefined;

  function receive(server: SpeakerEditsState) {
    state.update((s) => ({ ...s, status: "ready", server }));
    schedulePoll();
  }

  function schedulePoll() {
    clearTimeout(pollTimer);
    if (get(state).server?.state !== "applying") return;
    const current = generation;
    pollTimer = setTimeout(() => {
      if (current === generation) void refresh();
    }, pollMs);
  }

  async function refresh() {
    if (!load) return;
    const current = generation;
    try {
      const server = await load();
      if (current === generation) receive(server);
    } catch {
      // A failed poll keeps the last answer and tries again.
      if (current === generation) schedulePoll();
    }
  }

  // Opening keeps `shownRevision`: the recording is loaded beside the
  // session, and either may come first.
  async function open(loadWith: LoadSpeakerEdits | null, saveWith: SaveSpeakerEdits | null) {
    const current = ++generation;
    clearTimeout(pollTimer);
    load = loadWith;
    save = saveWith;
    if (!loadWith || !saveWith) {
      state.update((s) => ({ ...OFF, shownRevision: s.shownRevision }));
      return;
    }
    state.update((s) => ({ ...OFF, shownRevision: s.shownRevision, pending: emptyPending(), status: "loading" }));
    await firstLoad(current, 0);
  }

  // Until the operator has answered once the panel is read-only, so a failed
  // read is tried again, less and less often. A 404 is final: the meeting is
  // not this reader's, or the operator predates the route.
  async function firstLoad(current: number, attempt: number) {
    try {
      const server = await load!();
      if (current === generation) receive(server);
    } catch (error) {
      if (current !== generation) return;
      state.update((s) => ({ ...OFF, shownRevision: s.shownRevision, status: "failed" }));
      if (error instanceof SpeakerEditsError && error.code === "not-found") return;
      pollTimer = setTimeout(
        () => {
          if (current === generation) void firstLoad(current, attempt + 1);
        },
        Math.min(pollMs * 2 ** attempt, LOAD_RETRY_MAX_MS),
      );
    }
  }

  async function write(next: (doc: SpeakerEditsDoc) => SpeakerEditsDoc, clearPending: boolean) {
    const { server, saving } = get(state);
    if (!save || !server || saving) return false;
    const current = generation;
    state.update((s) => ({ ...s, saving: true, error: "" }));
    try {
      const answer = await save(server.revision, speakerEditsBody(next(server.doc)));
      if (current !== generation) return false;
      state.update((s) => ({ ...s, saving: false, pending: clearPending ? emptyPending() : s.pending }));
      receive(answer);
      return true;
    } catch (error) {
      if (current !== generation) return false;
      state.update((s) => ({ ...s, saving: false, error: describeSpeakerEditsError(error) }));
      if (error instanceof SpeakerEditsError && error.code === "revision-conflict") void refresh();
      return false;
    }
  }

  return {
    subscribe: state.subscribe,
    open,
    refresh,
    // "Several people used this device": the split, plus anything typed so far.
    separate: (speakerId: string, currentLabels: ReadonlyMap<string, string>) =>
      write((doc) => withSplit(applyPending(doc, get(state).pending, currentLabels), speakerId), true),
    // "Treat as one person again".
    unsplit: (speakerId: string, currentLabels: ReadonlyMap<string, string>) =>
      write((doc) => withoutSplit(applyPending(doc, get(state).pending, currentLabels), speakerId), true),
    // Save: every typed name and pick, in one request.
    saveEdits: (currentLabels: ReadonlyMap<string, string>) =>
      write((doc) => applyPending(doc, get(state).pending, currentLabels), true),
    // A failed apply is retried by saving the same document again: a new
    // revision is what makes the operator try once more.
    retry: () => write((doc) => doc, false),
    setLabel(speakerId: string, label: string) {
      state.update((s) => ({ ...s, pending: { ...s.pending, labels: { ...s.pending.labels, [speakerId]: label } } }));
    },
    setMerge(speakerId: string, into: string | null) {
      state.update((s) => ({ ...s, pending: { ...s.pending, merges: { ...s.pending.merges, [speakerId]: into } } }));
    },
    discard() {
      state.update((s) => ({ ...s, pending: emptyPending(), error: "" }));
    },
    // Something the panel did around the session failed, such as reloading
    // the recording; said where the session's own failures are.
    reportError(message: string) {
      state.update((s) => ({ ...s, error: message }));
    },
    dismissError() {
      state.update((s) => ({ ...s, error: "" }));
    },
    // A recording was loaded (`revision`: the speaker edits it was published
    // with, 0 for none) or unloaded (null).
    showing(revision: number | null) {
      state.update((s) => ({ ...s, shownRevision: revision }));
    },
    close() {
      generation++;
      clearTimeout(pollTimer);
      state.set(OFF);
    },
  };
}

export type SpeakersSession = ReturnType<typeof createSpeakersSession>;
