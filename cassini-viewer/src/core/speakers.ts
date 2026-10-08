import type { TranscriptSpeaker, TranscriptWordsV1 } from "./types";

// Voices separated from one participant's device are speakers like any other,
// with ids of the form "<device id>~<n>" (SubSpeakerID in
// cassini-go-recorder/internal/transcribe/diarize.go). A device id never
// contains "~", so the id alone says which device a voice came from, and the
// viewer groups voices without reading any provenance.

export function isVoiceId(id: string): boolean {
  return id.includes("~");
}

// The device a voice was separated from, or "" for a device.
export function voiceParent(id: string): string {
  const at = id.lastIndexOf("~");
  return at > 0 ? id.slice(0, at) : "";
}

// n for "<device>~n", or 0.
export function voiceNumber(id: string): number {
  const at = id.lastIndexOf("~");
  const n = at > 0 ? Number.parseInt(id.slice(at + 1), 10) : 0;
  return Number.isFinite(n) ? n : 0;
}

// The default label of a voice: "<device label> · Speaker n" (DefaultVoiceLabel).
const DEFAULT_VOICE_LABEL = /^(.*) · Speaker \d+$/;

export interface SpeakerGroup {
  device: TranscriptSpeaker;
  // Whether the device itself still speaks in this transcript. False once its
  // words all went to voices.
  speaks: boolean;
  // Voices separated from this device, by voice number.
  voices: TranscriptSpeaker[];
}

// groupSpeakers lists the devices in a meeting with their voices beneath them.
//
// `roster` is the loaded transcript's speakers, where a split device's voices
// stand in for it. `participants` is the original roster when the operator
// says what it was. `deviceNames` names split devices where nothing else
// does: the file's own record of them (a voice's "x-device" hint), for a
// reader with no operator behind it. Failing both, a split device's name is
// read back out of a voice's default label, and only when every voice has
// been renamed does the device go by a generic name.
export function groupSpeakers(
  roster: readonly TranscriptSpeaker[],
  participants: readonly TranscriptSpeaker[] = [],
  deviceNames: readonly TranscriptSpeaker[] = [],
): SpeakerGroup[] {
  const groups = new Map<string, SpeakerGroup>();
  const groupFor = (deviceId: string): SpeakerGroup => {
    let group = groups.get(deviceId);
    if (!group) {
      group = { device: { id: deviceId, label: "" }, speaks: false, voices: [] };
      groups.set(deviceId, group);
    }
    return group;
  };
  for (const participant of participants) {
    if (!isVoiceId(participant.id)) groupFor(participant.id).device.label = participant.label;
  }
  for (const speaker of roster) {
    const parent = voiceParent(speaker.id);
    if (parent) {
      groupFor(parent).voices.push(speaker);
      continue;
    }
    const group = groupFor(speaker.id);
    group.speaks = true;
    group.device.label ||= speaker.label;
  }
  const listed = [...groups.values()].filter((group) => group.speaks || group.voices.length > 0);
  const named = new Map(deviceNames.map((device) => [device.id, device.label]));
  for (const group of listed) {
    group.voices.sort((a, b) => voiceNumber(a.id) - voiceNumber(b.id));
    group.device.label ||= named.get(group.device.id) ?? "";
    if (!group.device.label) {
      const named = group.voices.map((voice) => DEFAULT_VOICE_LABEL.exec(voice.label)?.[1]).find(Boolean);
      group.device.label = named ?? "Shared device";
    }
  }
  return listed;
}

// The meeting's people, whichever transcript is shown: the roster with each
// split device left out wherever its voices are listed. On the original
// transcript of a separated meeting the roster names the device too (its
// words are credited to it there), but the people who used it are its voices,
// and they are who can be named, compared and listened to.
export function withoutSplitDevices(roster: readonly TranscriptSpeaker[]): TranscriptSpeaker[] {
  const split = new Set(roster.map((speaker) => voiceParent(speaker.id)).filter(Boolean));
  return roster.filter((speaker) => !split.has(speaker.id));
}

// How many people the groups add up to, counting a device whose words all went
// to voices as its voices and every other device as one person.
export function countPeople(groups: readonly SpeakerGroup[]): { voices: number; devices: number; split: boolean } {
  let voices = 0;
  let split = false;
  for (const group of groups) {
    voices += group.voices.length + (group.speaks ? 1 : 0);
    split ||= group.voices.length > 0;
  }
  return { voices, devices: groups.length, split };
}

export interface VoiceSample {
  // Total time the speaker's words cover.
  speechMs: number;
  // The start of the speaker's longest stretch with nobody else's words in it,
  // and where a sample of it stops: at most `maxMs` later.
  startMs: number;
  endMs: number;
}

export const VOICE_SAMPLE_MS = 6000;

// voiceSamples finds, for every speaker, the best few seconds to listen to:
// the longest run of their words, in time order, that no other speaker's word
// interrupts. That is the stretch most likely to be that voice alone, which is
// what a person naming it needs to hear.
export function voiceSamples(
  transcript: Pick<TranscriptWordsV1, "segments">,
  maxMs = VOICE_SAMPLE_MS,
): Map<string, VoiceSample> {
  const words: { speaker: string; startMs: number; endMs: number }[] = [];
  for (const segment of transcript.segments) {
    if (!segment.speaker) continue;
    for (const word of segment.words) {
      if (Number.isFinite(word.startMs) && Number.isFinite(word.endMs) && word.endMs >= word.startMs) {
        words.push({ speaker: segment.speaker, startMs: word.startMs, endMs: word.endMs });
      }
    }
  }
  words.sort((a, b) => a.startMs - b.startMs || a.endMs - b.endMs);

  const samples = new Map<string, VoiceSample>();
  const best = new Map<string, { startMs: number; endMs: number }>();
  let run: { speaker: string; startMs: number; endMs: number } | null = null;
  const close = () => {
    if (!run) return;
    const current = best.get(run.speaker);
    if (!current || run.endMs - run.startMs > current.endMs - current.startMs) {
      best.set(run.speaker, { startMs: run.startMs, endMs: run.endMs });
    }
  };
  for (const word of words) {
    const sample = samples.get(word.speaker) ?? { speechMs: 0, startMs: 0, endMs: 0 };
    sample.speechMs += word.endMs - word.startMs;
    samples.set(word.speaker, sample);
    if (run && run.speaker === word.speaker) {
      run.endMs = Math.max(run.endMs, word.endMs);
    } else {
      close();
      run = { ...word };
    }
  }
  close();
  for (const [speaker, sample] of samples) {
    const stretch = best.get(speaker)!;
    sample.startMs = stretch.startMs;
    sample.endMs = Math.min(stretch.endMs, stretch.startMs + maxMs);
  }
  return samples;
}
