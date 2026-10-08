import { describe, expect, it } from "vitest";

import {
  countPeople,
  groupSpeakers,
  isVoiceId,
  speakersWhoSpeak,
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
