import { vi } from "vitest";

import { buildTranscriptIndex } from "../../core/transcript";
import type { TranscriptSegment, TranscriptSpeaker, TranscriptWordsV1 } from "../../core/types";
import type { DataProvider, LoadMeetingOptions } from "../../viewer/dataProvider";
import type { MeetingCatalogEntry } from "../../viewer/catalog";
import type { LoadedArtifact } from "../../viewer/loadArtifact";
import type { SpeakersClock } from "./session";
import {
  SPEAKER_EDITS_FORMAT,
  SpeakerEditsError,
  type SpeakerEditsDoc,
  type SpeakerEditsPhase,
  type SpeakerEditsReport,
  type SpeakerEditsState,
} from "../../viewer/speakerEdits";

// A synthetic meeting: two Talk participants, one of them a meeting-room laptop
// three people spoke into. Invented words only.
export const ROOM = "spk_room";
export const BEN = "spk_ben";
const ROOM_LABEL = "Meeting room laptop";

// Who spoke when, as the diarizer would find it on the room's track: [voice,
// start, end] with 0 for Ben's own device.
const TURNS: [number, number, number][] = [
  [1, 0, 8000],
  [0, 8000, 10_000],
  [2, 10_000, 13_000],
  [1, 13_000, 19_000],
  [3, 19_000, 20_000],
  [2, 20_000, 23_000],
  [0, 23_000, 24_000],
];
const DURATION_MS = 24_000;
const VOCABULARY = ["lantern", "river", "paper", "festival", "evening", "boats", "light", "bridge"];

function words(startMs: number, endMs: number) {
  const out = [];
  for (let at = startMs, i = 0; at < endMs; at += 500, i++) {
    out.push({ text: VOCABULARY[(at / 500 + i) % VOCABULARY.length], startMs: at, endMs: at + 500 });
  }
  return out;
}

// How the room's device is spoken for in one version of the recording.
type Room = (voice: number) => string;

function transcript(room: Room, speakers: TranscriptSpeaker[]): TranscriptWordsV1 {
  const segments: TranscriptSegment[] = TURNS.map(([voice, startMs, endMs], index) => {
    const segmentWords = words(startMs, endMs);
    return {
      id: `s${index + 1}`,
      speaker: voice === 0 ? BEN : room(voice),
      startMs,
      endMs,
      text: segmentWords.map((word) => word.text).join(" "),
      words: segmentWords,
    };
  });
  return { version: "transcript.words.v1", media: { src: "", durationMs: DURATION_MS }, speakers, segments };
}

function artifact(source: TranscriptWordsV1, audioSrc: string): LoadedArtifact {
  return {
    transcript: source, index: buildTranscriptIndex(source), displayTranscript: null, readableTranscript: null,
    summary: null, audioSrc, captionsSrc: null, chaptersSrc: null,
    timingPrecision: { level: "word", label: "Word", detail: "" }, metadata: null,
    wordEndsBoundedByAudio: true, availableTranscripts: [], currentTranscriptId: "",
  };
}

const voice = (n: number, label = `${ROOM_LABEL} · Speaker ${n}`) => ({ id: `${ROOM}~${n}`, label });
const ben = { id: BEN, label: "Ben Ortiz" };

// The recording as published before anyone separated anything.
export const original = (audioSrc: string) =>
  artifact(transcript(() => ROOM, [{ id: ROOM, label: ROOM_LABEL }, ben]), audioSrc);
// The file names the device its voices came from (each voice's "x-device").
const splitDevices = [{ id: ROOM, label: ROOM_LABEL }];
// …after the room's voices were separated.
export const separated = (audioSrc: string): LoadedArtifact => ({
  ...artifact(transcript((n) => `${ROOM}~${n}`, [voice(1), ben, voice(2), voice(3)]), audioSrc),
  splitDevices,
});
// …after two of the voices were named, and nothing else changed.
export const renamed = (audioSrc: string): LoadedArtifact => ({
  ...artifact(transcript((n) => `${ROOM}~${n}`, [voice(1, "Mira"), ben, voice(2, "Leo"), voice(3)]), audioSrc),
  splitDevices,
});
// …after voice 3 was found to be voice 1, and both people named.
export const named = (audioSrc: string): LoadedArtifact => ({
  ...artifact(transcript((n) => `${ROOM}~${n === 3 ? 1 : n}`, [voice(1, "Mira"), ben, voice(2, "Leo")]), audioSrc),
  splitDevices,
});

// A separated recording also keeps its original transcript, which the reader
// can switch to. It credits the room's device, which the roster names just
// before its voices (as the portable reader does).
const TRANSCRIPTS = [
  { id: "separated-voices", label: "Separated voices", description: "", isDefault: true },
  { id: "raw-asr", label: "Original", description: "", isDefault: false },
];
export const separatedWithOriginal = (audioSrc: string): LoadedArtifact => ({
  ...separated(audioSrc),
  availableTranscripts: TRANSCRIPTS,
  currentTranscriptId: "separated-voices",
});
export const originalOfSeparated = (audioSrc: string): LoadedArtifact => ({
  ...artifact(transcript(() => ROOM, [{ id: ROOM, label: ROOM_LABEL }, voice(1), ben, voice(2), voice(3)]), audioSrc),
  splitDevices,
  availableTranscripts: TRANSCRIPTS,
  currentTranscriptId: "raw-asr",
});

export const meeting: MeetingCatalogEntry = {
  id: "m1", title: "Lantern festival planning", dateLabel: "2026-09-28", audioPath: "m1.opus",
  speakerCount: 2, segmentCount: TURNS.length, digestDurationMs: DURATION_MS,
};

export const splitReport = (voices: string[]): SpeakerEditsReport => ({
  splits: [{ speakerId: ROOM, voices, inconclusive: voices.length < 2 }],
  inconclusive: voices.length < 2 ? [ROOM] : [],
});

// A silent WAV the length of the meeting, so the player really plays and a
// sample really stops.
export function silentWav(durationMs = DURATION_MS, rate = 8000): string {
  const samples = Math.round((durationMs / 1000) * rate);
  const buffer = new ArrayBuffer(44 + samples);
  const view = new DataView(buffer);
  const text = (at: number, value: string) => [...value].forEach((char, i) => view.setUint8(at + i, char.charCodeAt(0)));
  text(0, "RIFF");
  view.setUint32(4, 36 + samples, true);
  text(8, "WAVE");
  text(12, "fmt ");
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);
  view.setUint16(22, 1, true);
  view.setUint32(24, rate, true);
  view.setUint32(28, rate, true);
  view.setUint16(32, 1, true);
  view.setUint16(34, 8, true);
  text(36, "data");
  view.setUint32(40, samples, true);
  new Uint8Array(buffer, 44).fill(128);
  return URL.createObjectURL(new Blob([buffer], { type: "audio/wav" }));
}

// A clock the test moves by hand: the panel's countdown and the operator's
// elapsed time both read it, and nothing waits on real time.
export function manualClock(start = 1_000_000) {
  let at = start;
  const ticks = new Set<{ every: number; due: number; tick: () => void }>();
  const clock: SpeakersClock = {
    now: () => at,
    every(ms, tick) {
      const entry = { every: ms, due: at + ms, tick };
      ticks.add(entry);
      return () => ticks.delete(entry);
    },
  };
  return {
    clock,
    advance(ms: number) {
      at += ms;
      for (const entry of [...ticks]) {
        if (entry.due > at) continue;
        while (entry.due <= at) entry.due += entry.every;
        entry.tick();
      }
    },
    ticking: () => ticks.size,
  };
}

// A controlled operator, not a second implementation of it: the test decides
// when an apply finishes and what the republished recording holds. Like the
// app's provider, it keeps the copy of the recording it read first for the
// rest of the tab, and reads the published one again only when asked to.
//
// With `estimateMs` it is an operator that says how far an apply is: the attempt
// is queued at the save, by `time`, in the phase the test last set (separating
// unless told otherwise), and the test moves it through the others.
// Without, it is an operator older than that, which says nothing.
export function speakersFixture(
  options: { available?: boolean; reason?: SpeakerEditsState["reason"]; estimateMs?: number } = {},
) {
  const audioSrc = silentWav();
  const time = manualClock();
  let phase: SpeakerEditsPhase = "separating";
  let queuedAt = 0;
  let published = original(audioSrc);
  let cached: LoadedArtifact | null = null;
  let available = options.available ?? true;
  let reason = options.reason ?? "";
  let revision = 0;
  let appliedRevision = 0;
  let state: SpeakerEditsState["state"] = available ? "idle" : "unavailable";
  let lastError = "";
  let doc: SpeakerEditsDoc = { format: SPEAKER_EDITS_FORMAT, revision: 0, splits: [], merges: [], labels: [] };
  let report: SpeakerEditsReport | null = null;

  const progress = () =>
    options.estimateMs === undefined
      ? undefined
      : state === "applying"
        ? { phase, elapsedMs: time.clock.now() - queuedAt, estimatedMs: options.estimateMs }
        : null;
  const snapshot = (): SpeakerEditsState => structuredClone({
    available, reason, revision, appliedRevision, state, lastError,
    doc, participants: [{ id: ROOM, label: ROOM_LABEL }, ben], report, progress: progress(),
  });
  const load = vi.fn(async (_entry: MeetingCatalogEntry) => snapshot());
  const save = vi.fn(async (_entry: MeetingCatalogEntry, expectRevision: number, body: SpeakerEditsDoc) => {
    if (expectRevision !== revision) throw new SpeakerEditsError(409, "revision-conflict", { revision });
    if (state === "applying") throw new SpeakerEditsError(409, "busy");
    revision++;
    doc = { ...structuredClone(body), format: SPEAKER_EDITS_FORMAT, revision };
    state = "applying";
    lastError = "";
    queuedAt = time.clock.now();
    return snapshot();
  });
  const loadMeeting = vi.fn(async (_entry: MeetingCatalogEntry, options?: LoadMeetingOptions) => {
    if (options?.fresh || !cached) cached = published;
    return cached;
  });

  const provider: DataProvider = {
    loadCatalog: async () => ({ version: "cassini.viewer.catalog.v1", meetings: [meeting] }),
    loadMeetingForEntry: loadMeeting,
    loadMeetingSummary: async () => null,
    loadBundledArtifact: async () => published,
    switchTranscript: async (_entry: MeetingCatalogEntry, id: string) =>
      id === "raw-asr" ? originalOfSeparated(audioSrc) : published,
    loadSpeakerEdits: load,
    saveSpeakerEdits: save,
  };

  return {
    provider, load, save, loadMeeting, audioSrc, time,
    // Where the operator's apply is now.
    phase(next: SpeakerEditsPhase) {
      phase = next;
    },
    // The operator republished the recording with the saved revision.
    applied(next: (audio: string) => LoadedArtifact, nextReport: SpeakerEditsReport | null = report) {
      published = next(audioSrc);
      // As the producer records it: only a recording whose speakers the
      // edits changed says which edits it was published with.
      if (published.transcript.speakers.some((speaker) => speaker.id.includes("~"))) {
        published = { ...published, speakerEditsRevision: revision };
      }
      appliedRevision = revision;
      report = nextReport;
      state = available ? "idle" : "unavailable";
    },
    // What the operator says about the meeting from now on.
    availability(nowAvailable: boolean, why: SpeakerEditsState["reason"]) {
      available = nowAvailable;
      reason = why;
      if (state === "idle" || state === "unavailable") state = available ? "idle" : "unavailable";
    },
    // The reader opens the meeting in a new tab: the copy read before is gone,
    // and the next read is of the published recording.
    forget() {
      cached = null;
    },
    failed(error: string) {
      state = "failed";
      lastError = error;
    },
    dispose() {
      URL.revokeObjectURL(audioSrc);
    },
  };
}
