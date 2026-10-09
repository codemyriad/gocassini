import { describe, expect, it } from "vitest";

import {
  applySpeakerOverlay,
  countPeople,
  groupSpeakers,
  isVoiceId,
  speakerOverlayFromKey,
  speakerOverlayKey,
  splitDevicesIn,
  voiceNumber,
  voiceParent,
  voiceSamples,
  withoutSplitDevices,
} from "./speakers";
import type { TranscriptSegment } from "./types";

describe("voice ids", () => {
  it("reads the device and the number out of a voice id", () => {
    expect(isVoiceId("spk_room_ab12~2")).toBe(true);
    expect(isVoiceId("spk_room_ab12")).toBe(false);
    expect(voiceParent("spk_room_ab12~2")).toBe("spk_room_ab12");
    expect(voiceParent("spk_room_ab12")).toBe("");
    expect(voiceNumber("spk_room_ab12~12")).toBe(12);
    expect(voiceNumber("spk_room_ab12")).toBe(0);
    // The LAST "~" separates, as VoiceParent does on the producer's side.
    expect(voiceParent("a~b~3")).toBe("a~b");
  });
});

describe("groupSpeakers", () => {
  it("lists every device once when nothing is split", () => {
    const groups = groupSpeakers([
      { id: "ana", label: "Ana" },
      { id: "room", label: "Meeting room laptop" },
    ]);
    expect(groups).toEqual([
      { device: { id: "ana", label: "Ana" }, speaks: true, voices: [] },
      { device: { id: "room", label: "Meeting room laptop" }, speaks: true, voices: [] },
    ]);
    expect(countPeople(groups)).toEqual({ voices: 2, devices: 2, split: false });
  });

  it("puts a split device's voices beneath it, by voice number, in the original order", () => {
    const groups = groupSpeakers(
      [
        { id: "room~2", label: "Leo" },
        { id: "ana", label: "Ana" },
        { id: "room~1", label: "Meeting room laptop · Speaker 1" },
      ],
      [
        { id: "room", label: "Meeting room laptop" },
        { id: "ana", label: "Ana" },
      ],
    );
    expect(groups.map((group) => [group.device.label, group.speaks, group.voices.map((voice) => voice.id)])).toEqual([
      ["Meeting room laptop", false, ["room~1", "room~2"]],
      ["Ana", true, []],
    ]);
    expect(countPeople(groups)).toEqual({ voices: 3, devices: 2, split: true });
  });

  it("names a split device from a voice's default label when nobody said what it was", () => {
    const groups = groupSpeakers([
      { id: "room~1", label: "Mira" },
      { id: "room~2", label: "Meeting room laptop · Speaker 2" },
    ]);
    expect(groups[0].device).toEqual({ id: "room", label: "Meeting room laptop" });
  });

  it("falls back to a generic name once every voice of an unlisted device is renamed", () => {
    const groups = groupSpeakers([
      { id: "room~1", label: "Mira" },
      { id: "room~2", label: "Leo" },
    ]);
    expect(groups[0].device).toEqual({ id: "room", label: "Shared device" });
  });

  it("keeps a device that still speaks beside its voices", () => {
    const groups = groupSpeakers([
      { id: "room", label: "Meeting room laptop" },
      { id: "room~1", label: "Meeting room laptop · Speaker 1" },
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0].speaks).toBe(true);
    expect(countPeople(groups).voices).toBe(2);
  });

  it("leaves out participants who said nothing in this transcript", () => {
    const groups = groupSpeakers([{ id: "ana", label: "Ana" }], [
      { id: "ana", label: "Ana" },
      { id: "ben", label: "Ben" },
    ]);
    expect(groups.map((group) => group.device.id)).toEqual(["ana"]);
  });
});

function segment(id: string, speaker: string, words: [number, number][]): TranscriptSegment {
  return {
    id,
    speaker,
    startMs: words[0][0],
    endMs: words.at(-1)![1],
    text: words.map(() => "w").join(" "),
    words: words.map(([startMs, endMs]) => ({ text: "w", startMs, endMs })),
  };
}

describe("voiceSamples", () => {
  it("picks each speaker's longest uninterrupted run and totals their speech", () => {
    const samples = voiceSamples({
      segments: [
        segment("a1", "a", [[0, 500], [500, 1000]]),
        segment("b1", "b", [[1000, 1400]]),
        segment("a2", "a", [[2000, 2500], [2500, 3000], [3000, 4000]]),
      ],
    });
    expect(samples.get("a")).toEqual({ speechMs: 3000, startMs: 2000, endMs: 4000 });
    expect(samples.get("b")).toEqual({ speechMs: 400, startMs: 1000, endMs: 1400 });
  });

  it("joins one speaker's consecutive segments into one run", () => {
    const samples = voiceSamples({
      segments: [segment("a1", "a", [[0, 1000]]), segment("a2", "a", [[1500, 3000]])],
    });
    expect(samples.get("a")).toMatchObject({ startMs: 0, endMs: 3000 });
  });

  it("breaks a run where another speaker's word falls inside it, whatever segment order says", () => {
    // Segments are not sorted by time in a produced transcript.
    const samples = voiceSamples({
      segments: [
        segment("a1", "a", [[0, 1000], [1000, 2000], [5000, 9000]]),
        segment("b1", "b", [[2100, 2200]]),
      ],
    });
    expect(samples.get("a")).toMatchObject({ startMs: 5000, endMs: 9000 });
  });

  it("breaks a run at a long pause, so a sample is not mostly silence", () => {
    // A few words at the start, nothing from anyone for a minute, then a
    // stretch of speech: the stretch is the sample, not the start.
    const samples = voiceSamples({
      segments: [segment("a1", "a", [[0, 400], [60_000, 61_000], [61_000, 63_500]])],
    });
    expect(samples.get("a")).toEqual({ speechMs: 3900, startMs: 60_000, endMs: 63_500 });
  });

  it("stops a sample after six seconds", () => {
    const samples = voiceSamples({ segments: [segment("a1", "a", [[1000, 20_000]])] });
    expect(samples.get("a")).toEqual({ speechMs: 19_000, startMs: 1000, endMs: 7000 });
  });
});

describe("withoutSplitDevices", () => {
  it("leaves out a device wherever its voices are listed, and keeps the voices", () => {
    // The original transcript of a separated meeting names the device too.
    const roster = [
      { id: "ana", label: "Ana" },
      { id: "room", label: "Meeting room" },
      { id: "room~1", label: "Mira" },
      { id: "room~2", label: "Leo" },
      { id: "ben", label: "Ben" },
    ];
    expect(withoutSplitDevices(roster).map((speaker) => speaker.id)).toEqual(["ana", "room~1", "room~2", "ben"]);
    const groups = groupSpeakers(withoutSplitDevices(roster));
    expect(groups.map((group) => [group.device.id, group.speaks, group.voices.length])).toEqual([
      ["ana", true, 0],
      ["room", false, 2],
      ["ben", true, 0],
    ]);
    expect(countPeople(groups)).toEqual({ voices: 4, devices: 3, split: true });
  });

  it("changes nothing where no device was split", () => {
    const roster = [{ id: "ana", label: "Ana" }, { id: "ben", label: "Ben" }];
    expect(withoutSplitDevices(roster)).toEqual(roster);
  });
});

describe("groupSpeakers device names", () => {
  const named = [
    { id: "room~1", label: "Mira" },
    { id: "room~2", label: "Leo" },
  ];
  it("names a split device from the file's record of it when no participant list says", () => {
    expect(groupSpeakers(named)[0].device.label).toBe("Shared device");
    expect(groupSpeakers(named, [], [{ id: "room", label: "Meeting room" }])[0].device.label).toBe("Meeting room");
  });

  it("prefers the operator's participant list to the file's record", () => {
    const groups = groupSpeakers(named, [{ id: "room", label: "Room 2" }], [{ id: "room", label: "Meeting room" }]);
    expect(groups[0].device.label).toBe("Room 2");
  });

  it("lists no group for a named device that has neither words nor voices", () => {
    expect(groupSpeakers([{ id: "ben", label: "Ben" }], [], [{ id: "room", label: "Meeting room" }])).toEqual([
      { device: { id: "ben", label: "Ben" }, speaks: true, voices: [] },
    ]);
  });
});

describe("applySpeakerOverlay", () => {
  // A meeting room laptop split into three voices, and Ben on his own device,
  // as derived from the recording: blocks carry their own copy of the label.
  const speakers = [
    { id: "room~1", label: "Room · Speaker 1" },
    { id: "ben", label: "Ben audio" },
    { id: "room~2", label: "Room · Speaker 2" },
    { id: "room~3", label: "Room · Speaker 3" },
  ];
  const block = (id: string, speaker: string, speakerLabel: string) => ({ id, speaker, speakerLabel, text: id });
  const blocks = [
    block("b1", "room~1", "Room · Speaker 1"),
    block("b2", "ben", "Ben"),
    block("b3", "room~3", "Room · Speaker 3"),
    block("b4", "room~2", "Room · Speaker 2"),
  ];

  it("gives the blocks and the roster the saved names, and copies only what changes", () => {
    const overlay = { labels: [{ speakerId: "room~1", label: "Mira audio" }, { speakerId: "nobody", label: "X" }], merges: [] };
    const result = applySpeakerOverlay(speakers, blocks, overlay);
    expect(result.speakers.map((speaker) => speaker.label)).toEqual([
      "Mira audio", "Ben audio", "Room · Speaker 2", "Room · Speaker 3",
    ]);
    // Labels on blocks are normalized as the derivation does it.
    expect(result.segments.map((segment) => segment.speakerLabel)).toEqual([
      "Mira", "Ben", "Room · Speaker 3", "Room · Speaker 2",
    ]);
    expect(result.segments[1]).toBe(blocks[1]);
    expect(result.speakers[1]).toBe(speakers[1]);
  });

  it("moves a merged voice's words to the voice it is the same person as, so its turns join", () => {
    const overlay = { labels: [{ speakerId: "room~1", label: "Mira" }], merges: [{ from: "room~3", into: "room~1" }] };
    const result = applySpeakerOverlay(speakers, blocks, overlay);
    expect(result.segments.map((segment) => [segment.speaker, segment.speakerLabel])).toEqual([
      ["room~1", "Mira"], ["ben", "Ben"], ["room~1", "Mira"], ["room~2", "Room · Speaker 2"],
    ]);
    // The merged voice is nobody of its own any more.
    expect(result.speakers.map((speaker) => speaker.id)).toEqual(["room~1", "ben", "room~2"]);
    // Without a name, the merged words take the target's own label.
    const unnamed = applySpeakerOverlay(speakers, blocks, { labels: [], merges: [{ from: "room~3", into: "room~2" }] });
    expect(unnamed.segments[2]).toEqual(block("b3", "room~2", "Room · Speaker 2"));
  });

  it("ignores a merge across devices, into a voice that is not here, or into a merged voice", () => {
    const merges = [
      { from: "room~2", into: "ben" },
      { from: "room~3", into: "room~9" },
      { from: "other~1", into: "room~1" },
    ];
    const result = applySpeakerOverlay(speakers, blocks, { labels: [], merges });
    expect(result.segments).toEqual(blocks);
    expect(result.speakers).toEqual(speakers);
    // A chain is refused as the operator refuses it.
    const chained = applySpeakerOverlay(speakers, blocks, {
      labels: [], merges: [{ from: "room~3", into: "room~2" }, { from: "room~2", into: "room~1" }],
    });
    expect(chained.segments.map((segment) => segment.speaker)).toEqual(["room~1", "ben", "room~3", "room~1"]);
  });

  it("reassigns segments that carry no label of their own", () => {
    const segments = [{ speaker: "room~3", startMs: 0 }, { speaker: "room~1", startMs: 1 }];
    const result = applySpeakerOverlay(speakers, segments, { labels: [], merges: [{ from: "room~3", into: "room~1" }] });
    expect(result.segments).toEqual([{ speaker: "room~1", startMs: 0 }, { speaker: "room~1", startMs: 1 }]);
    expect("speakerLabel" in result.segments[0]!).toBe(false);
  });

  it("puts a name that is no longer saved back to its default, as the operator will", () => {
    // The recording on screen was published with voice 1 named Mira and Ben
    // renamed; the saved edits name neither any more.
    const shown = [
      { id: "room~1", label: "Mira" },
      { id: "ben", label: "Benjamin" },
      { id: "room~2", label: "Room · Speaker 2" },
      { id: "other~4", label: "Kim" },
    ];
    const shownBlocks = [block("b1", "room~1", "Mira"), block("b2", "ben", "Benjamin"), block("b3", "other~4", "Kim")];
    const base = [{ speakerId: "room", label: "Room" }, { speakerId: "ben", label: "Ben audio" }];
    const result = applySpeakerOverlay(shown, shownBlocks, { labels: [], merges: [], base });
    expect(result.speakers.map((speaker) => speaker.label)).toEqual([
      "Room · Speaker 1", "Ben audio", "Room · Speaker 2", "other · Speaker 4",
    ]);
    expect(result.segments.map((segment) => segment.speakerLabel)).toEqual(["Room · Speaker 1", "Ben", "other · Speaker 4"]);
    // Unchanged speakers are the very same objects.
    expect(result.speakers[2]).toBe(shown[2]);
    // A saved name still wins over the default.
    const named = applySpeakerOverlay(shown, shownBlocks, { labels: [{ speakerId: "room~1", label: "Mira" }], merges: [], base });
    expect(named.segments[0]).toBe(shownBlocks[0]);
    // Without a base nothing is known about defaults, and nothing is changed.
    const unknown = applySpeakerOverlay(shown, shownBlocks, { labels: [], merges: [], base: [] });
    expect(unknown.segments).toBe(shownBlocks);
    expect(speakerOverlayKey({ labels: [], merges: [], base })).not.toBe("");
  });

  it("hands back the very arrays when there is nothing to show", () => {
    expect(applySpeakerOverlay(speakers, blocks, null).segments).toBe(blocks);
    const empty = applySpeakerOverlay(speakers, blocks, { labels: [], merges: [] });
    expect(empty.segments).toBe(blocks);
    expect(empty.speakers).toBe(speakers);
  });

  it("keys an overlay by what it says, not by its order", () => {
    const a = { labels: [{ speakerId: "b", label: "B" }, { speakerId: "a", label: "A" }], merges: [] };
    const b = { labels: [{ speakerId: "a", label: "A" }, { speakerId: "b", label: "B" }], merges: [] };
    expect(speakerOverlayKey(a)).toBe(speakerOverlayKey(b));
    expect(speakerOverlayKey({ labels: [], merges: [] })).toBe("");
    expect(speakerOverlayKey(null)).toBe("");
    expect(speakerOverlayFromKey(speakerOverlayKey(a))).toEqual(b);
    expect(speakerOverlayFromKey("")).toBeNull();
  });

  it("finds the devices a roster has voices for", () => {
    expect([...splitDevicesIn(speakers)]).toEqual(["room"]);
    expect(splitDevicesIn([{ id: "room", label: "Room" }]).size).toBe(0);
  });
});
