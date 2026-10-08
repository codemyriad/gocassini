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
import { isVoiceId, voiceParent, type SpeakerOverlay } from "../../core/speakers";

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
  // The last revision the operator said it had applied, with its document:
  // what the published recording says once a later save has failed.
  applied: { revision: number; doc: SpeakerEditsDoc } | null;
  // The recording is being read again because its speakers were separated
  // differently ("Voices updated" without the reader asking).
  reloading: boolean;
  error: string;
  // When the operator's last answer arrived and what time it is now, by the
  // session's clock. `now` moves once a second while an apply with progress
  // runs, so the time left counts down between polls.
  receivedAt: number;
  now: number;
}

// Time as the session sees it, so tests can move it by hand.
export interface SpeakersClock {
  now(): number;
  // Calls `tick` every `ms` until the returned function is called.
  every(ms: number, tick: () => void): () => void;
}

export const systemClock: SpeakersClock = {
  now: () => Date.now(),
  every(ms, tick) {
    const timer = setInterval(tick, ms);
    return () => clearInterval(timer);
  },
};

export const PROGRESS_TICK_MS = 1000;

const OFF: SpeakersState = {
  status: "off",
  server: null,
  shownRevision: null,
  pending: emptyPending(),
  saving: false,
  applied: null,
  reloading: false,
  error: "",
  receivedAt: 0,
  now: 0,
};

// How long ago, by the session's clock, the operator said how far it was.
export function sinceAnswer({ receivedAt, now }: Pick<SpeakersState, "receivedAt" | "now">): number {
  return Math.max(0, now - receivedAt);
}

// How often the operator is asked how far it is while it applies a saved
// revision. A rename is republished in a few seconds, so a slower cadence
// would leave the reader waiting on the poll rather than on the operator.
export const SPEAKER_POLL_MS = 1000;
// The first wait before reading a meeting's speaker edits again after the
// first read failed, doubled at every failure up to the longest.
export const SPEAKER_RETRY_MS = 3000;
const LOAD_RETRY_MAX_MS = 60_000;

// Whether the recording on screen is older than what the operator applied.
// Only a different split makes that worth reading again (segmentationBehind).
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

// Whether the recording on screen is separated differently from the one the
// operator published: a device split or made one person again since, or a
// voice said to be a different person again. Names and merges never make it
// so — the overlay shows them on the recording on screen — but a different
// split cuts the words differently, and a voice merged in the recording on
// screen cannot be cut back out of the one it joined: only the republished
// recording has them. `shownSpeakers` are the ids of the speakers the
// recording on screen has.
export function segmentationBehind(
  state: Pick<SpeakersState, "server" | "shownRevision">,
  shownSpeakers: ReadonlySet<string>,
): boolean {
  if (!recordingBehind(state)) return false;
  const server = state.server!;
  const splits = (server.report?.splits ?? []).filter((split) => !split.inconclusive && split.voices.length > 1);
  const published = new Set(splits.map((split) => split.speakerId));
  const shownVoices = [...shownSpeakers].filter(isVoiceId);
  const shownSplits = new Set(shownVoices.map(voiceParent));
  if (published.size !== shownSplits.size || [...published].some((id) => !shownSplits.has(id))) return true;
  // The voices the published recording has: the ones found, less those merged
  // into another found voice by the edits it was published with. Only the
  // applied document says which those are.
  if (server.revision !== server.appliedRevision) return false;
  const found = new Set(splits.flatMap((split) => split.voices));
  const voices = new Set(found);
  for (const { from, into } of server.doc.merges) if (found.has(from) && found.has(into)) voices.delete(from);
  const shown = new Set(shownVoices);
  return [...voices].some((id) => !shown.has(id));
}

// The saved names and merges the recording on screen does not have yet, to be
// shown on it (applySpeakerOverlay) until a recording published with them is
// loaded: the revision being applied, or the one applied, when it is newer
// than the recording's own. A failed apply saved nothing new into the
// recording, so it falls back to the last revision the operator applied, or
// to nothing: the page never shows names the recording will not have.
export function speakerOverlayFor({
  server,
  shownRevision,
  applied,
}: Pick<SpeakersState, "server" | "shownRevision" | "applied">): SpeakerOverlay | null {
  if (!server || shownRevision === null) return null;
  const saved =
    server.state === "applying" || server.revision === server.appliedRevision
      ? { revision: server.revision, doc: server.doc }
      : applied;
  if (!saved || saved.revision <= shownRevision) return null;
  // A name taken away goes back to its default, which the original roster
  // says: the recording on screen may still carry the name.
  const base = (server.participants ?? []).map(({ id, label }) => ({ speakerId: id, label }));
  return { labels: saved.doc.labels, merges: saved.doc.merges, base };
}

// One meeting's speaker edits: the operator's state, the reader's unsaved
// edits, and polling while the operator applies a saved revision. Every write
// is the whole desired document with the revision it was made from, so there is
// no queue to reconcile: a conflict means someone else saved first, and the
// answer is to show what they saved.
export function createSpeakersSession(options: { pollMs?: number; retryMs?: number; clock?: SpeakersClock } = {}) {
  const pollMs = options.pollMs ?? SPEAKER_POLL_MS;
  const retryMs = options.retryMs ?? SPEAKER_RETRY_MS;
  const clock = options.clock ?? systemClock;
  const state = writable<SpeakersState>(OFF);
  let load: LoadSpeakerEdits | null = null;
  let save: SaveSpeakerEdits | null = null;
  let generation = 0;
  let pollTimer: ReturnType<typeof setTimeout> | undefined;
  let stopTicking: (() => void) | null = null;

  function receive(server: SpeakerEditsState) {
    const at = clock.now();
    const applied =
      server.state !== "applying" && server.state !== "failed" && server.appliedRevision === server.revision
        ? { revision: server.revision, doc: server.doc }
        : null;
    state.update((s) => ({ ...s, status: "ready", server, applied: applied ?? s.applied, receivedAt: at, now: at }));
    schedulePoll();
    tickWhileApplying();
  }

  // The clock ticks only while there is a time left to count down.
  function tickWhileApplying() {
    const server = get(state).server;
    const counting = server?.state === "applying" && Boolean(server.progress);
    if (counting && !stopTicking) {
      stopTicking = clock.every(PROGRESS_TICK_MS, () => state.update((s) => ({ ...s, now: clock.now() })));
    } else if (!counting) {
      stopTick();
    }
  }

  function stopTick() {
    stopTicking?.();
    stopTicking = null;
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
    stopTick();
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
        Math.min(retryMs * 2 ** attempt, LOAD_RETRY_MAX_MS),
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
    // "Separate voices": the split, plus anything typed so far.
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
    // The recording is (or is no longer) being read again on its own.
    reloading(reloading: boolean) {
      state.update((s) => ({ ...s, reloading }));
    },
    close() {
      generation++;
      clearTimeout(pollTimer);
      stopTick();
      state.set(OFF);
    },
  };
}

export type SpeakersSession = ReturnType<typeof createSpeakersSession>;
