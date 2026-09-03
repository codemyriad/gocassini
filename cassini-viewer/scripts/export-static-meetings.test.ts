import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

import { describe, expect, it } from "vitest";

import {
  artifactContentFields,
  buildDisplayTranscriptFromArtifacts,
  describeMeeting,
  describeSpeechToTextVariant,
  preferredPortableTitle,
  portableContentFields,
  portableRoomFields,
  describeVariantSuffix,
  buildReadableTranscriptFromPortable,
  buildTranscriptWordsFromPortable,
  parseArgs,
  validatePublishedPortableManifest,
} from "./export-static-meetings.mjs";

function writePublishedPortableProbeFixture(
  sourceDir: string,
  meetingId: string,
  meeting: Record<string, unknown>,
  // What the file holds, for the tests that care: the transcript's words and
  // the summary the producer sealed in. Both default to what a minimal fixture
  // has always had — an empty transcript and no summary.
  contents: { words?: string[]; summaryMarkdown?: string } = {},
) {
  const encode = (value: unknown) => {
    const raw = Buffer.from(JSON.stringify(value), "utf8");
    const gzip = gzipSync(raw);
    return {
      chunk: gzip.toString("base64url"),
      sha256: createHash("sha256").update(raw).digest("hex"),
      rawBytes: raw.byteLength,
      gzipBytes: gzip.byteLength,
    };
  };
  const words = contents.words ?? [];
  const body = encode({
    format: "cassini.words.v1",
    wordCount: words.length,
    items: words.map((text, index) => ({
      id: `w_${index}`,
      text,
      startMs: index * 500,
      endMs: index * 500 + 400,
    })),
  });
  const manifest = encode({
    kind: "cassini-portable-meeting",
    version: 1,
    profile: "ogg-opus",
    meeting,
    integrity: {
      matchPolicy: "exact-opus-audio-v1",
      opusAudioSha256: "a".repeat(64),
    },
    transcripts: [{
      id: "raw-asr",
      role: "raw-asr",
      default: true,
      format: "cassini.words.v1",
      wordCount: words.length,
      payloadRef: {
        prefix: "CASSINI_TX_RAW_ASR_PAYLOAD_",
        chunkCount: 1,
        sha256: body.sha256,
        rawBytes: body.rawBytes,
        gzipBytes: body.gzipBytes,
        mime: "application/vnd.cassini.transcript-words+json",
        encoding: "base64url+gzip+utf8json",
      },
    }],
    ...(contents.summaryMarkdown === undefined ? {} : {
      attachments: [{
        name: "summary.md",
        mime: "text/markdown",
        contentBase64: Buffer.from(contents.summaryMarkdown, "utf8").toString("base64"),
      }],
    }),
  });
  writeFileSync(join(sourceDir, `${meetingId}.opus`), "");
  writeFileSync(
    join(sourceDir, `${meetingId}.opus.ffprobe.json`),
    JSON.stringify({
      format: { tags: {
        CASSINI_FORMAT: "org.cassini.portable-meeting/1",
        CASSINI_PROFILE: "ogg-opus",
        CASSINI_PAYLOAD_MIME: "application/vnd.cassini.portable-meeting+json",
        CASSINI_PAYLOAD_ENCODING: "base64url+gzip+utf8json",
        CASSINI_PAYLOAD_SCHEMA:
          "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json",
        CASSINI_PAYLOAD_CHUNK_COUNT: "1",
        CASSINI_PAYLOAD_SHA256: manifest.sha256,
        CASSINI_PAYLOAD_RAW_BYTES: String(manifest.rawBytes),
        CASSINI_PAYLOAD_GZIP_BYTES: String(manifest.gzipBytes),
        CASSINI_PAYLOAD_000: manifest.chunk,
        CASSINI_AUDIO_MATCH_POLICY: "exact-opus-audio-v1",
        CASSINI_AUDIO_OPUS_SHA256: "a".repeat(64),
        CASSINI_TX_RAW_ASR_PAYLOAD_000: body.chunk,
      } },
      streams: [],
    }),
  );
}

describe("describeMeeting", () => {
  it("parses colon-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-05--12:38:29")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-05 12:38",
    });
  });

  it("parses dash-separated legacy meeting ids", () => {
    expect(describeMeeting("daily-meeting--2026-03-04--12-36-53")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-04 12:36",
    });
  });

  it("parses double-dash ids that keep date inside title", () => {
    expect(describeMeeting("daily-meeting-2026-03-10--12:30")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-10 12:30",
    });
  });

  it("parses compact stamped meeting ids", () => {
    expect(describeMeeting("synthetic-pied-piper-v1--20260310T150453")).toEqual({
      title: "Synthetic Pied Piper V1",
      dateLabel: "2026-03-10 15:04",
    });
  });

  it("ignores trailing stt variant suffixes when parsing meeting ids", () => {
    expect(describeMeeting("daily-meeting-2026-03-10--12:30--stt-whisper-large-v3")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-10 12:30",
    });
  });

  it("derives the date from ULID meeting ids instead of echoing the id", () => {
    // 01KKA70QN0 encodes 2026-03-09T21:11:00Z.
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS")).toEqual({
      title: "Untitled meeting",
      dateLabel: "2026-03-09 21:11",
    });
  });

  it("handles ULID meeting ids with stt variant suffixes", () => {
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS--stt-whisper-large-v3")).toEqual({
      title: "Untitled meeting",
      dateLabel: "2026-03-09 21:11",
    });
  });

  it("keeps the plain-id fallback for ULID lookalikes with implausible timestamps", () => {
    expect(describeMeeting("00000000000000000000000000")).toEqual({
      title: "00000000000000000000000000",
      dateLabel: "00000000000000000000000000",
    });
  });

  it("does not treat lowercase ids as ULIDs", () => {
    // Operator job ids are always uppercase; lowercase stays a plain name.
    expect(describeMeeting("01kka70qn0abcdefghjkmnpqrs")).toEqual({
      title: "01kka70qn0abcdefghjkmnpqrs",
      dateLabel: "01kka70qn0abcdefghjkmnpqrs",
    });
  });

  it("does not treat 25- or 27-char Crockford strings as ULIDs", () => {
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQR").dateLabel).toBe(
      "01KKA70QN0ABCDEFGHJKMNPQR",
    );
    expect(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRST").dateLabel).toBe(
      "01KKA70QN0ABCDEFGHJKMNPQRST",
    );
  });

  it("keeps the plain-id fallback for non-ULID meeting ids", () => {
    expect(describeMeeting("weekly-sync")).toEqual({
      title: "Weekly Sync",
      dateLabel: "weekly-sync",
    });
  });

  it("prefers a date-only id over pack processing time (D-685)", () => {
    expect(describeMeeting("daily-meeting-2026-04-08", "2026-08-29T09:14:07Z")).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-04-08",
    });
  });

  it("prefers recordedAtLocal — when the meeting happened — over everything else (D-685)", () => {
    // A pack rebuilt on 29 Aug still describes a meeting held on 10 Mar.
    expect(
      describeMeeting(
        "daily-meeting-2026-03-10--12:30",
        "2026-08-29T09:14:07Z",
        "2026-03-10T12:30:00",
      ),
    ).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-10 12:30",
    });
  });

  it("keeps recordedAtLocal's own wall-clock digits, with no timezone shift", () => {
    // The value carries no zone, so it must not be round-tripped through Date:
    // a UTC render on a CET exporter would move 12:30 to 11:30.
    expect(
      describeMeeting("daily-meeting-2026-04-08", "", "2026-04-08T00:15:00").dateLabel,
    ).toBe("2026-04-08 00:15");
  });

  it("prefers a filename timestamp over pack createdAtUtc (D-685)", () => {
    // createdAtUtc is when the pack was WRITTEN. Reprocessing the archive
    // rewrites it to the rebuild day, which is never the meeting's date; the
    // id's own stamp is a claim about the recording and outranks it.
    expect(
      describeMeeting("daily-meeting--2026-03-05--12:38:29", "2026-08-29T09:14:07Z"),
    ).toEqual({
      title: "Daily Meeting",
      dateLabel: "2026-03-05 12:38",
    });
  });

  it("prefers a ULID job id's recording time over pack createdAtUtc (D-685)", () => {
    expect(
      describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS", "2026-08-29T09:14:07Z").dateLabel,
    ).toBe(describeMeeting("01KKA70QN0ABCDEFGHJKMNPQRS").dateLabel);
  });

  it("ignores an unparseable or out-of-range recordedAtLocal", () => {
    expect(
      describeMeeting("weekly-sync", "2026-04-08T07:31:02Z", "not-a-timestamp")
        .dateLabel,
    ).toBe("2026-04-08 07:31");
    expect(
      describeMeeting("weekly-sync", "2026-04-08T07:31:02Z", "2026-13-40T99:99:00")
        .dateLabel,
    ).toBe("2026-04-08 07:31");
    expect(
      describeMeeting("weekly-sync", "2026-04-08T07:31:02Z", "2026-02-30T12:00:00")
        .dateLabel,
    ).toBe("2026-04-08 07:31");
    expect(
      describeMeeting("weekly-sync", "2026-04-08T07:31:02Z", "2026-04-08T12:00:99junk")
        .dateLabel,
    ).toBe("2026-04-08 07:31");
  });

  it("falls back to the id when createdAtUtc is missing or unparseable", () => {
    expect(describeMeeting("weekly-sync")).toEqual({
      title: "Weekly Sync",
      dateLabel: "weekly-sync",
    });
    expect(describeMeeting("weekly-sync", "not-a-timestamp").dateLabel).toBe(
      "weekly-sync",
    );
    expect(describeMeeting("weekly-sync", "   ").dateLabel).toBe(
      "weekly-sync",
    );
  });
});

describe("portableRoomFields", () => {
  it("carries the published room id", () => {
    expect(
      portableRoomFields({ meeting: { roomId: " rm_9f2a1c3d4e5b6a70 " } }),
    ).toEqual({ roomId: "rm_9f2a1c3d4e5b6a70" });
  });

  it("omits the keys entirely rather than writing empty strings", () => {
    // An entry with roomId: "" would read as "this meeting has a room whose id
    // is the empty string", and every consumer would have to check presence AND
    // emptiness. A meeting with no room is a normal state, not a broken one.
    expect(portableRoomFields({ meeting: { roomId: "   " } })).toEqual({});
    expect(portableRoomFields({ meeting: {} })).toEqual({});
    expect(portableRoomFields({})).toEqual({});
    expect(portableRoomFields(null)).toEqual({});
  });

  it("carries the job and attempt that produced the artifact", () => {
    expect(
      portableRoomFields({
        meeting: { roomId: "rm_9f2a1c3d4e5b6a70", jobId: " 01K3Q7W8ZC9F0MJXQ2NB8V4RTD ", attemptNumber: 2 },
      }),
    ).toEqual({
      roomId: "rm_9f2a1c3d4e5b6a70",
      jobId: "01K3Q7W8ZC9F0MJXQ2NB8V4RTD",
      attemptNumber: 2,
    });
  });

  it("treats a non-positive or non-integer attempt as not recorded", () => {
    // Attempts are 1-based, so a zero is "nobody told us" and not an attempt
    // that could have produced anything.
    for (const attemptNumber of [0, -1, 1.5, "2", null]) {
      expect(portableRoomFields({ meeting: { jobId: "01ABC", attemptNumber } })).toEqual({
        jobId: "01ABC",
      });
    }
  });

});

describe("portableContentFields", () => {
  const summaryAttachment = (markdown: string) => ({
    name: "summary.md",
    mime: "text/markdown",
    contentBase64: Buffer.from(markdown, "utf8").toString("base64"),
  });

  const withWords = (count: number) => ({
    transcripts: [{ id: "raw-asr", role: "raw-asr", default: true, wordCount: count }],
  });

  it("reports the sealed summary and the default transcript's word count", () => {
    expect(
      portableContentFields({
        ...withWords(3468),
        attachments: [summaryAttachment("# Weekly Sync\n\n- Ship it.\n")],
      }),
    ).toEqual({ hasSummary: true, wordCount: 3468 });
  });

  it("says false, not nothing, when the file carries no summary", () => {
    // The exporter read the file, so "no summary" is a fact it can state. Only
    // an archive published before D-716 has nothing to say, and it says that by
    // having no key at all.
    expect(portableContentFields(withWords(12))).toEqual({ hasSummary: false, wordCount: 12 });
    expect(portableContentFields({ ...withWords(12), attachments: [] })).toEqual({
      hasSummary: false,
      wordCount: 12,
    });
  });

  it("treats an empty or undecodable summary attachment as no summary", () => {
    // Matches readPortableSummaryMarkdown: a damaged attachment costs the
    // summary, not the export.
    for (const attachment of [
      summaryAttachment("   \n"),
      { name: "summary.md", contentBase64: "////" },
      { name: "summary.md", contentBase64: 7 },
      { name: "notes.md", contentBase64: Buffer.from("# Notes").toString("base64") },
      null,
    ]) {
      expect(portableContentFields({ ...withWords(1), attachments: [attachment] })).toEqual({
        hasSummary: false,
        wordCount: 1,
      });
    }
  });

  it("matches the summary attachment by name, case-insensitively", () => {
    expect(
      portableContentFields({
        ...withWords(1),
        attachments: [{ name: " Summary.MD ", contentBase64: Buffer.from("# S").toString("base64") }],
      }),
    ).toEqual({ hasSummary: true, wordCount: 1 });
  });

  it("counts the decoded body when the manifest entry declares no wordCount", () => {
    // The packer writes wordCount with omitempty, so a genuinely empty
    // transcript and an unstated count look the same on the wire; the decoded
    // items settle it.
    expect(
      portableContentFields({
        transcripts: [{ id: "raw-asr", role: "raw-asr", default: true }],
        transcript: { items: [{ text: "one" }, { text: "two" }] },
      }),
    ).toEqual({ hasSummary: false, wordCount: 2 });
    expect(
      portableContentFields({
        transcripts: [{ id: "raw-asr", role: "raw-asr", default: true }],
        transcript: { items: [] },
      }),
    ).toEqual({ hasSummary: false, wordCount: 0 });
  });

  it("counts the body, not the index entry, when the two disagree", () => {
    // The only case where the choice matters. Every decoded item is one word
    // and becomes one segment, so the entry's own segmentCount is already this
    // number — publishing the declared one would put two answers to the same
    // question in one catalog entry, and contradict the bundle the agent gets.
    expect(
      portableContentFields({
        transcripts: [{ id: "raw-asr", role: "raw-asr", default: true, wordCount: 1200 }],
        transcript: { items: [{ text: "one" }, { text: "two" }] },
      }),
    ).toEqual({ hasSummary: false, wordCount: 2 });
  });

  it("counts the DEFAULT transcript, not the first or the longest", () => {
    // The count has to be the one `cassini meetings context` reports for the
    // same meeting, or a Prepare total disagrees with the bundle it produces.
    expect(
      portableContentFields({
        transcripts: [
          { id: "canary", role: "raw-asr", wordCount: 900 },
          { id: "parakeet", role: "raw-asr", default: true, wordCount: 1200 },
        ],
      }),
    ).toEqual({ hasSummary: false, wordCount: 1200 });
  });

  it("omits wordCount entirely when nothing states one", () => {
    // Absent means unknown. A 0 here would tell the Prepare panel this meeting
    // contributes nothing to the total, which is a different claim.
    expect(portableContentFields({ transcripts: [{ id: "raw-asr", role: "raw-asr" }] })).toEqual({
      hasSummary: false,
    });
    expect(portableContentFields({})).toEqual({ hasSummary: false });
    expect(portableContentFields(null)).toEqual({ hasSummary: false });
  });

  it("ignores a wordCount that is not a count", () => {
    for (const wordCount of ["1200", -3, 1.5, null]) {
      expect(
        portableContentFields({
          transcripts: [{ id: "raw-asr", role: "raw-asr", default: true, wordCount }],
        }),
      ).toEqual({ hasSummary: false });
    }
  });
});

describe("artifactContentFields", () => {
  it("reads both facts out of a directory pack's manifest.json", () => {
    expect(
      artifactContentFields({
        files: { audio: "meeting.webm", summary: "summary.md" },
        wordCount: 3468,
      }),
    ).toEqual({ hasSummary: true, wordCount: 3468 });
  });

  it("says no summary when the pack never generated one", () => {
    // files.summary is written only when a summary exists
    // (internal/transcribe/format.go), so its absence is an answer.
    expect(artifactContentFields({ files: { audio: "meeting.webm" }, wordCount: 0 })).toEqual({
      hasSummary: false,
      wordCount: 0,
    });
  });

  it("omits wordCount for a manifest that states none", () => {
    expect(artifactContentFields({ files: { audio: "meeting.webm" } })).toEqual({
      hasSummary: false,
    });
  });

  it("claims nothing about a pack it found no manifest for", () => {
    // readManifest returns {} when there is no manifest.json, and "we have no
    // metadata" must not be published as "this meeting has no summary".
    expect(artifactContentFields({})).toEqual({});
    expect(artifactContentFields(null)).toEqual({});
    expect(artifactContentFields({ wordCount: 12 })).toEqual({ wordCount: 12 });
  });
});

describe("preferredPortableTitle", () => {
  const ulid = "01KKA70QN0ABCDEFGHJKMNPQRS";

  it("uses a real embedded title (e.g. the Talk room name)", () => {
    expect(preferredPortableTitle({ meeting: { title: "Daily Meeting" } }, ulid)).toBe(
      "Daily Meeting",
    );
    expect(preferredPortableTitle({ meeting: { title: "  Silvio-Alex-Chris  " } }, ulid)).toBe(
      "Silvio-Alex-Chris",
    );
  });

  it("rejects packer defaults that echo the meeting id", () => {
    expect(preferredPortableTitle({ meeting: { title: ulid } }, ulid)).toBe("");
    expect(
      preferredPortableTitle({ meeting: { title: ulid } }, `${ulid}--stt-whisper-large-v3`),
    ).toBe("");
    expect(preferredPortableTitle({ meeting: { title: "Cassini Meeting" } }, ulid)).toBe("");
  });

  it("rejects missing or empty titles", () => {
    expect(preferredPortableTitle({ meeting: { title: "   " } }, ulid)).toBe("");
    expect(preferredPortableTitle({ meeting: {} }, ulid)).toBe("");
    expect(preferredPortableTitle({}, ulid)).toBe("");
    expect(preferredPortableTitle(null, ulid)).toBe("");
  });
});

// Pre-existing grab-bag suite: STT variant labels and portable transcript
// grouping, kept under the original suite name.
describe("describeMeeting", () => {
  it("formats whisper STT variants for catalog titles", () => {
    expect(
      describeSpeechToTextVariant({
        provenance: {
          speechToText: {
            backend: "local-whisper",
            engine: "faster-whisper",
            model: "large-v3",
          },
        },
      }),
    ).toBe("Whisper large-v3");
  });

  it("formats non-whisper STT variants for catalog titles", () => {
    expect(
      describeSpeechToTextVariant({
        provenance: {
          speechToText: {
            backend: "http",
            engine: "parakeet-nemo",
            model: "nvidia/parakeet-tdt-0.6b-v2",
          },
        },
      }),
    ).toBe("Parakeet tdt-0.6b-v2");
  });

  it("falls back to the stt suffix when provenance is absent", () => {
    expect(describeVariantSuffix("daily-meeting-2026-03-10--12:30--stt-whisper-small-en")).toBe(
      "Whisper Small En",
    );
  });

  it("groups published word items into multi-word readable blocks", () => {
    const portable = {
      meeting: { durationMs: 10_000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 0, endMs: 400, text: "okay" },
          { id: "seg_2", speaker: "spk_1", startMs: 400, endMs: 800, text: "this" },
          { id: "seg_3", speaker: "spk_1", startMs: 800, endMs: 1200, text: "looks" },
          { id: "seg_4", speaker: "spk_1", startMs: 1200, endMs: 1600, text: "much" },
          { id: "seg_5", speaker: "spk_1", startMs: 1600, endMs: 2000, text: "better" },
          { id: "seg_6", speaker: "spk_1", startMs: 3600, endMs: 4000, text: "now" },
        ],
      },
    };
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 10_000, sha256: "" },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable, transcript);

    expect(readable.segments).toHaveLength(1);
    expect(readable.segments[0]).toMatchObject({
      text: "okay this looks much better now",
      sourceSegmentIds: ["seg_1", "seg_2", "seg_3", "seg_4", "seg_5", "seg_6"],
    });
  });

  it("rejects a transcript body item containing more than one word", () => {
    expect(() => buildTranscriptWordsFromPortable({
      meeting: { durationMs: 1000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      transcript: {
        items: [{ speaker: "spk_1", startMs: 0, endMs: 500, text: "two words" }],
      },
    })).toThrow("portable transcript item 0 must contain exactly one word");
  });

  it("keeps a hard break across large pauses or speaker changes", () => {
    const portable = {
      meeting: { durationMs: 20_000 },
      speakers: [
        { id: "spk_1", label: "Chris" },
        { id: "spk_2", label: "Alex" },
      ],
      transcript: {
        items: [
          { id: "seg_1", speaker: "spk_1", startMs: 0, endMs: 500, text: "Okay," },
          { id: "seg_2", speaker: "spk_1", startMs: 500, endMs: 750, text: "that" },
          { id: "seg_3", speaker: "spk_1", startMs: 750, endMs: 1000, text: "works." },
          { id: "seg_4", speaker: "spk_1", startMs: 6000, endMs: 6250, text: "Long" },
          { id: "seg_5", speaker: "spk_1", startMs: 6250, endMs: 6500, text: "pause." },
          { id: "seg_6", speaker: "spk_2", startMs: 7000, endMs: 7300, text: "Other" },
          { id: "seg_7", speaker: "spk_2", startMs: 7300, endMs: 7600, text: "speaker." },
        ],
      },
    };
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 20_000, sha256: "" },
      speakers: portable.speakers,
      segments: portable.transcript.items.map((item) => ({
        ...item,
        words: [{ id: `${item.id}:w_0`, text: item.text, startMs: item.startMs, endMs: item.endMs }],
      })),
    };

    const readable = buildReadableTranscriptFromPortable(portable, transcript);

    expect(readable.segments).toHaveLength(3);
    expect(readable.segments[0]?.text).toBe("Okay, that works.");
    expect(readable.segments[1]?.text).toBe("Long pause.");
    expect(readable.segments[2]?.text).toBe("Other speaker.");
  });

  it("keeps an interrupted block whole even with exact transcript words (D-690)", () => {
    const chimaWords = [
      ["Actually,", 54_981, 55_381],
      ["I", 55_381, 55_541],
      ["was", 55_541, 55_701],
      ["wondering", 55_701, 56_182],
      ["if", 56_182, 56_342],
      ["I", 56_342, 56_582],
      ["should", 56_582, 56_822],
      ["have", 56_822, 57_062],
      ["pinged", 57_062, 57_462],
      ["you", 57_462, 57_702],
      ["in", 57_701, 57_941],
      ["the", 57_942, 58_102],
      ["afternoon,", 58_102, 58_742],
      ["and", 58_742, 58_901],
      ["I", 58_901, 59_061],
      ["didn't", 59_061, 59_462],
      ["because", 59_462, 59_861],
      ["I", 59_861, 60_022],
      ["was", 60_022, 60_261],
      ["busy", 60_261, 60_621],
      ["doing", 60_621, 61_021],
      ["something", 61_021, 61_541],
      ["else.", 61_541, 62_021],
      ["It's", 77_541, 77_861],
      ["a", 77_861, 78_021],
      ["pity,", 78_021, 78_502],
      ["Chris,", 78_501, 79_061],
      ["you're", 79_061, 79_542],
      ["ruining", 79_542, 80_102],
      ["everything.", 80_102, 80_742],
    ];
    const portable = {
      meeting: { durationMs: 110_000 },
      speakers: [
        { id: "spk_chima", label: "chima" },
        { id: "spk_silvio", label: "Silvio" },
      ],
      transcript: {
        items: [
          ...chimaWords.map(([text, startMs, endMs], index) => ({
            id: `seg_chima_${index}`,
            speaker: "spk_chima",
            startMs,
            endMs,
            text,
          })),
          ...[
            ["Telling", 64_837, 65_470],
            ["Mattia", 65_470, 66_102],
            ["off", 66_102, 66_735],
            ["about", 66_735, 67_368],
            ["homework.", 67_368, 68_001],
          ].map(([text, startMs, endMs], index) => ({
            id: `seg_silvio_${index}`,
            speaker: "spk_silvio",
            startMs,
            endMs,
            text,
          })),
        ],
      },
      readableTranscript: {
        version: "transcript.readable.v1",
        speakers: [
          { id: "spk_chima", label: "chima" },
          { id: "spk_silvio", label: "Silvio" },
        ],
        segments: [
          {
            id: "readable_000002",
            speaker: "spk_chima",
            startMs: 54_981,
            endMs: 83_541,
            text: "Actually, I was wondering if I should have pinged you in the afternoon, and I didn't because I was busy doing something else. It's a pity, Chris, you're ruining everything.",
            sourceSegmentIds: chimaWords.map((_, index) => `seg_chima_${index}`),
            words: [
              "Actually,","I","was","wondering","if","I","should","have","pinged","you","in","the","afternoon,","and","I","didn't","because","I","was","busy","doing","something","else.","It's","a","pity,","Chris,","you're","ruining","everything.",
            ].map((text, index, words) => ({
              text,
              startMs: 54_981 + Math.floor(((83_541 - 54_981) * index) / words.length),
              endMs: 54_981 + Math.floor(((83_541 - 54_981) * (index + 1)) / words.length),
            })),
          },
          {
            id: "readable_000003",
            speaker: "spk_silvio",
            startMs: 64_837,
            endMs: 78_757,
            text: "Telling Mattia off about homework.",
            sourceSegmentIds: [0, 1, 2, 3, 4].map((index) => `seg_silvio_${index}`),
          },
        ],
      },
    };

    const transcript = buildTranscriptWordsFromPortable(portable);
    const readable = buildReadableTranscriptFromPortable(portable, transcript);
    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const chimaBlocks = display.blocks.filter((block) => block.speaker === "spk_chima");

    // Exact transcript words do not cut the readable block in two; the viewer
    // annotates the interruption without rewriting the cleaned prose.
    expect(chimaBlocks).toHaveLength(1);
    expect(chimaBlocks[0]?.text).toContain("doing something else.");
    expect(chimaBlocks[0]?.text).toContain("It's a pity, Chris, you're ruining everything.");
    expect(chimaBlocks[0]?.startMs).toBe(54_981);
    expect(chimaBlocks[0]?.endMs).toBe(80_742);
  });

  it("builds a viewer-ready display transcript with timed cleaned tokens", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2400,
          text: "um hello there i think",
          words: [
            { id: "w_1", text: "um", startMs: 1000, endMs: 1100 },
            { id: "w_2", text: "hello", startMs: 1200, endMs: 1500 },
            { id: "w_3", text: "there", startMs: 1520, endMs: 1800 },
            { id: "w_4", text: "i", startMs: 1850, endMs: 1900 },
            { id: "w_5", text: "think", startMs: 1920, endMs: 2200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2400,
          text: "Hello there, I think.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

    expect(display.version).toBe("transcript.display.v1");
    expect(display.blocks).toHaveLength(1);
    expect(display.blocks[0]?.tokens.map((token) => token.text)).toEqual([
      "Hello",
      "there",
      ",",
      "I",
      "think",
      ".",
    ]);
    expect(display.blocks[0]?.tokens[0]).toMatchObject({
      sourceWordIds: ["w_2"],
      startMs: 1200,
      endMs: 1500,
      alignment: "source",
    });
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });

  it("falls back to time-overlap words when readable sourceSegmentIds are stale", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 12000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: [
        "okay",
        "any",
        "work",
        "with",
        "these",
        "comments",
        "is",
        "probably",
        "worth",
        "it",
      ].map((word, index) => ({
        id: `seg_${String(index + 1).padStart(6, "0")}`,
        speaker: "spk_1",
        startMs: 1000 + index * 1000,
        endMs: 1500 + index * 1000,
        text: word,
        words: [
          {
            id: `w_${String(index + 1).padStart(6, "0")}`,
            text: word,
            startMs: 1000 + index * 1000,
            endMs: 1500 + index * 1000,
          },
        ],
      })),
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 12000 },
      speakers: [{ id: "spk_1", label: "Chris" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 11000,
          text: "Okay, any work with these comments is probably worth it.",
          sourceSegmentIds: ["seg_000001", "seg_000002"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const block = display.blocks[0];
    const probablyToken = block.tokens.find((token) => token.text === "probably");

    expect(block.endMs).toBeGreaterThanOrEqual(9000);
    expect(block.timedWordCount).toBe(block.wordCount);
    expect(probablyToken?.startMs).toBeGreaterThanOrEqual(8000);
  });

  it("leaves synonym substitutions untimed instead of assigning fake precision", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "we should buy this now",
          words: [
            { id: "w_1", text: "we", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "should", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "buy", startMs: 1700, endMs: 2200 },
            { id: "w_4", text: "this", startMs: 2200, endMs: 2700 },
            { id: "w_5", text: "now", startMs: 2700, endMs: 3200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "We should purchase this now.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const purchaseToken = display.blocks[0]?.tokens.find((token) => token.text === "purchase");

    expect(purchaseToken?.alignment).toBe("none");
    expect(purchaseToken?.sourceWordIds).toEqual([]);
    expect(purchaseToken?.startMs).toBeUndefined();
    expect(purchaseToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(4);
    expect(display.blocks[0]?.wordCount).toBe(5);
    expect(display.blocks[0]?.timingCoverage).toBe(0.8);
  });

  it("leaves edge-only rewritten cleaned words untimed without two-sided anchors", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3000,
          text: "yeah because it was fine",
          words: [
            { id: "w_1", text: "yeah", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "because", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "it", startMs: 1700, endMs: 1900 },
            { id: "w_4", text: "was", startMs: 1900, endMs: 2200 },
            { id: "w_5", text: "fine", startMs: 2200, endMs: 2600 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3000,
          text: "Please publish this. Yeah, because it was fine. Later extras.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];
    const publishToken = wordTokens.find((token) => token.text === "publish");
    const yeahToken = wordTokens.find((token) => token.text === "Yeah");
    const laterToken = wordTokens.find((token) => token.text === "Later");

    expect(publishToken?.startMs).toBeUndefined();
    expect(publishToken?.endMs).toBeUndefined();
    expect(publishToken?.alignment).toBe("none");
    expect(yeahToken?.startMs).toBe(1000);
    expect(yeahToken?.endMs).toBe(1300);
    expect(yeahToken?.alignment).toBe("source");
    expect(laterToken?.startMs).toBeUndefined();
    expect(laterToken?.endMs).toBeUndefined();
    expect(laterToken?.alignment).toBe("none");
    expect(display.blocks[0]?.timedWordCount).toBe(5);
    expect(display.blocks[0]?.wordCount).toBe(10);
    expect(display.blocks[0]?.timingCoverage).toBe(0.5);
  });

  it("leaves fully rewritten cleaned blocks untimed at the word level", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 127398,
          endMs: 128197,
          text: "Mexicans",
          words: [
            { id: "w_1", text: "Mexicans", startMs: 127398, endMs: 128197 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Silvio" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 127398,
          endMs: 128197,
          text: "Makes sense.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const wordTokens = display.blocks[0]?.tokens.filter((token) => token.kind === "word") ?? [];

    expect(wordTokens).toMatchObject([
      {
        text: "Makes",
        alignment: "none",
      },
      {
        text: "sense",
        alignment: "none",
      },
    ]);
    expect(wordTokens.every((token) => token.startMs === undefined && token.endMs === undefined)).toBe(true);
    expect(display.blocks[0]?.timedWordCount).toBe(0);
    expect(display.blocks[0]?.wordCount).toBe(2);
    expect(display.blocks[0]?.timingCoverage).toBe(0);
  });

  it("leaves inserted cleaned words untimed when the ASR gap has no lexical overlap", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "we should buy now",
          words: [
            { id: "w_1", text: "we", startMs: 1000, endMs: 1300 },
            { id: "w_2", text: "should", startMs: 1300, endMs: 1700 },
            { id: "w_3", text: "buy", startMs: 1700, endMs: 2200 },
            { id: "w_4", text: "now", startMs: 2700, endMs: 3200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 6000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 3500,
          text: "We should buy it now.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const insertedToken = display.blocks[0]?.tokens.find((token) => token.text === "it");

    expect(insertedToken?.alignment).toBe("none");
    expect(insertedToken?.sourceWordIds).toEqual([]);
    expect(insertedToken?.startMs).toBeUndefined();
    expect(insertedToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(4);
    expect(display.blocks[0]?.wordCount).toBe(5);
    expect(display.blocks[0]?.timingCoverage).toBe(0.8);
  });

  it("leaves paraphrased runs untimed when exact anchor overlap is weak", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "to it if they need to",
          words: [
            { id: "w_1", text: "to", startMs: 1000, endMs: 1200 },
            { id: "w_2", text: "it", startMs: 1200, endMs: 1400 },
            { id: "w_3", text: "if", startMs: 1400, endMs: 1600 },
            { id: "w_4", text: "they", startMs: 1600, endMs: 1800 },
            { id: "w_5", text: "need", startMs: 1800, endMs: 2000 },
            { id: "w_6", text: "to", startMs: 2000, endMs: 2200 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "To approach it using greenfield to determine.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const usingToken = display.blocks[0]?.tokens.find((token) => token.text === "using");
    const greenfieldToken = display.blocks[0]?.tokens.find((token) => token.text === "greenfield");

    expect(usingToken?.alignment).toBe("none");
    expect(usingToken?.startMs).toBeUndefined();
    expect(usingToken?.endMs).toBeUndefined();
    expect(greenfieldToken?.alignment).toBe("none");
    expect(greenfieldToken?.startMs).toBeUndefined();
    expect(greenfieldToken?.endMs).toBeUndefined();
  });

  it("synthesizes source word ids for transcript artifact words that omit them", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2200,
          text: "hello there",
          words: [
            { text: "hello", startMs: 1000, endMs: 1500 },
            { text: "there", startMs: 1500, endMs: 2000 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2200,
          text: "Hello there.",
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

    expect(display.blocks[0]?.tokens[0]).toMatchObject({
      text: "Hello",
      sourceWordIds: ["seg_000000:w_0"],
      startMs: 1000,
      endMs: 1500,
      alignment: "source",
    });
    expect(display.blocks[0]?.tokens[1]).toMatchObject({
      text: "there",
      sourceWordIds: ["seg_000000:w_1"],
      startMs: 1500,
      endMs: 2000,
      alignment: "source",
    });
    expect(display.blocks[0]?.timingCoverage).toBe(1);
  });

  it("leaves contraction-expansion edge tokens untimed when there is no exact ASR match", () => {
    const transcript = {
      version: "transcript.words.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "seg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "we're not done",
          words: [
            { id: "w_1", text: "we're", startMs: 1000, endMs: 1500 },
            { id: "w_2", text: "not", startMs: 1500, endMs: 1900 },
            { id: "w_3", text: "done", startMs: 1900, endMs: 2400 },
          ],
        },
      ],
    };
    const readable = {
      version: "transcript.readable.v1",
      media: { src: "meeting.opus", durationMs: 4000 },
      speakers: [{ id: "spk_1", label: "Alice" }],
      segments: [
        {
          id: "rseg_1",
          speaker: "spk_1",
          startMs: 1000,
          endMs: 2600,
          text: "We are not done.",
          sourceSegmentIds: ["seg_1"],
        },
      ],
    };

    const display = buildDisplayTranscriptFromArtifacts(transcript, readable);
    const weToken = display.blocks[0]?.tokens.find((token) => token.text === "We");
    const areToken = display.blocks[0]?.tokens.find((token) => token.text === "are");

    expect(weToken?.alignment).toBe("none");
    expect(weToken?.startMs).toBeUndefined();
    expect(weToken?.endMs).toBeUndefined();
    expect(areToken?.alignment).toBe("none");
    expect(areToken?.startMs).toBeUndefined();
    expect(areToken?.endMs).toBeUndefined();
    expect(display.blocks[0]?.timedWordCount).toBe(2);
    expect(display.blocks[0]?.wordCount).toBe(4);
    expect(display.blocks[0]?.timingCoverage).toBe(0.5);
  });
});

describe("parseArgs", () => {
  it("returns null recordingsBaseUrl when flag is absent", () => {
    const { recordingsBaseUrl } = parseArgs([]);
    expect(recordingsBaseUrl).toBeNull();
  });

  it("parses --recordings-base-url and normalises trailing slash", () => {
    const { recordingsBaseUrl } = parseArgs([
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
  });

  it("preserves trailing slash when already present", () => {
    const { recordingsBaseUrl } = parseArgs([
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main/",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
  });

  it("throws when --recordings-base-url has no value", () => {
    expect(() => parseArgs(["--recordings-base-url"])).toThrow(
      "missing value for --recordings-base-url",
    );
  });

  it("parses --recordings-base-url alongside --source-dir and --output-dir", () => {
    const { recordingsBaseUrl, sourceDir, outputDir } = parseArgs([
      "--source-dir",
      "/tmp/source",
      "--output-dir",
      "/tmp/out",
      "--recordings-base-url",
      "https://view.meetings.codemyriad.io/main/",
    ]);
    expect(recordingsBaseUrl).toBe("https://view.meetings.codemyriad.io/main/");
    expect(sourceDir).toContain("/tmp/source");
    expect(outputDir).toContain("/tmp/out");
  });

  // D-531: publish is lightweight by default; the viewer shell (index.html +
  // assets/) is embedded only on --rebuild-viewer.
  it("rebuildViewer defaults to false", () => {
    expect(parseArgs([]).rebuildViewer).toBe(false);
  });

  it("parses --rebuild-viewer as a value-less boolean", () => {
    expect(parseArgs(["--rebuild-viewer"]).rebuildViewer).toBe(true);
  });

  it("parses --rebuild-viewer alongside --source-dir/--output-dir without consuming a value", () => {
    const { rebuildViewer, sourceDir, outputDir } = parseArgs([
      "--rebuild-viewer",
      "--source-dir",
      "/tmp/source",
      "--output-dir",
      "/tmp/out",
    ]);
    expect(rebuildViewer).toBe(true);
    expect(sourceDir).toContain("/tmp/source");
    expect(outputDir).toContain("/tmp/out");
  });
});

describe("CLI entry point (export-static-meetings.mjs run directly)", () => {
  const scriptPath = fileURLToPath(new URL("./export-static-meetings.mjs", import.meta.url));

  // Regression for D-462: main() must run after every module-level `const` is
  // initialized. The helper-import tests above can never catch an eval-time
  // temporal-dead-zone crash (they never run main()); only executing the file
  // as a real entry point does. Before the fix this exited non-zero with
  // "Cannot access 'ULID_PATTERN' before initialization", breaking every publish.
  it("publishes a ULID-named meeting without a temporal-dead-zone crash", () => {
    // 01KKA70QN0... encodes 2026-03-09T21:11:00Z (see the describeMeeting unit test).
    const meetingId = "01KKA70QN0ABCDEFGHJKMNPQRS";
    const root = mkdtempSync(join(tmpdir(), "cassini-export-cli-"));
    try {
      const distDir = join(root, "dist");
      mkdirSync(join(distDir, "assets"), { recursive: true });
      writeFileSync(join(distDir, "index.html"), "<!doctype html><title>viewer</title>");

      const sourceDir = join(root, "source");
      // An empty directory is a valid minimal meeting: readManifest() returns {}.
      mkdirSync(join(sourceDir, meetingId), { recursive: true });
      const outputDir = join(root, "out");

      execFileSync(
        process.execPath,
        [
          scriptPath,
          "--source-dir",
          sourceDir,
          "--output-dir",
          outputDir,
          // Avoids needing real audio/transcript files to copy.
          "--recordings-base-url",
          "https://example.test/",
        ],
        { env: { ...process.env, CASSINI_VIEWER_DIST_DIR: distDir }, encoding: "utf8" },
      );

      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      expect(catalog.meetings).toHaveLength(1);
      expect(catalog.meetings[0]).toMatchObject({
        id: meetingId,
        title: "Untitled meeting",
        dateLabel: "2026-03-09 21:11",
      });
      // D-531: default publish is lightweight — no viewer shell embedded, even
      // when a dist is present (it is served from the Docker image at runtime).
      expect(existsSync(join(outputDir, "index.html"))).toBe(false);
      expect(existsSync(join(outputDir, "assets"))).toBe(false);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  // D-531: the lightweight default must not require the viewer dist at all —
  // publish should succeed with no built shell available.
  it("default lightweight run succeeds without any viewer dist", () => {
    const meetingId = "01KKA70QN0ABCDEFGHJKMNPQRS";
    const root = mkdtempSync(join(tmpdir(), "cassini-export-nodist-"));
    try {
      // Point the dist dir at an empty location (no index.html) to prove the
      // default path never reads it.
      const distDir = join(root, "dist");
      mkdirSync(distDir, { recursive: true });

      const sourceDir = join(root, "source");
      mkdirSync(join(sourceDir, meetingId), { recursive: true });
      const outputDir = join(root, "out");

      execFileSync(
        process.execPath,
        [
          scriptPath,
          "--source-dir",
          sourceDir,
          "--output-dir",
          outputDir,
          "--recordings-base-url",
          "https://example.test/",
        ],
        { env: { ...process.env, CASSINI_VIEWER_DIST_DIR: distDir }, encoding: "utf8" },
      );

      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      expect(catalog.meetings).toHaveLength(1);
      expect(existsSync(join(outputDir, "index.html"))).toBe(false);
      expect(existsSync(join(outputDir, "assets"))).toBe(false);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  // D-531: --rebuild-viewer restores the old behavior — embed the built shell.
  it("--rebuild-viewer embeds the viewer shell from the dist", () => {
    const meetingId = "01KKA70QN0ABCDEFGHJKMNPQRS";
    const root = mkdtempSync(join(tmpdir(), "cassini-export-rebuild-"));
    try {
      const distDir = join(root, "dist");
      mkdirSync(join(distDir, "assets"), { recursive: true });
      writeFileSync(join(distDir, "index.html"), "<!doctype html><title>viewer</title>");
      writeFileSync(join(distDir, "assets", "app.js"), "console.log('viewer')");

      const sourceDir = join(root, "source");
      mkdirSync(join(sourceDir, meetingId), { recursive: true });
      const outputDir = join(root, "out");

      execFileSync(
        process.execPath,
        [
          scriptPath,
          "--source-dir",
          sourceDir,
          "--output-dir",
          outputDir,
          "--recordings-base-url",
          "https://example.test/",
          "--rebuild-viewer",
        ],
        { env: { ...process.env, CASSINI_VIEWER_DIST_DIR: distDir }, encoding: "utf8" },
      );

      // Shell present and derived from the dist.
      expect(readFileSync(join(outputDir, "index.html"), "utf8")).toContain("<title>viewer</title>");
      expect(existsSync(join(outputDir, "assets", "app.js"))).toBe(true);
      // Catalog + meetings still produced.
      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      expect(catalog.meetings).toHaveLength(1);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  // D-685: date-only slug ids are still recording-time claims. They must win
  // over a portable's createdAtUtc even though no start time is recoverable.
  it("derives dateLabel from date-only ids for slug-named portable packs", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-export-createdat-"));
    try {
      const distDir = join(root, "dist");
      mkdirSync(distDir, { recursive: true });

      // extractPortableManifest() shells out to ffprobe, which the UI test
      // environment does not provide. Stub it with a script that emits the
      // pre-baked report stored next to the probed .opus file.
      const stubBinDir = join(root, "bin");
      mkdirSync(stubBinDir, { recursive: true });
      const stubPath = join(stubBinDir, "ffprobe");
      writeFileSync(stubPath, '#!/bin/sh\nfor arg in "$@"; do last="$arg"; done\nexec cat "${last}.ffprobe.json"\n');
      chmodSync(stubPath, 0o755);

      const sourceDir = join(root, "source");
      mkdirSync(sourceDir, { recursive: true });
      writePublishedPortableProbeFixture(sourceDir, "daily-meeting-2026-04-08", {
        id: "daily-meeting-2026-04-08",
        title: "Cassini Meeting",
        createdAtUtc: "2026-04-08T07:31:02Z",
      });
      writePublishedPortableProbeFixture(sourceDir, "daily-meeting-2026-04-09", {
        id: "daily-meeting-2026-04-09",
        title: "Cassini Meeting",
      });

      const outputDir = join(root, "out");
      execFileSync(
        process.execPath,
        [
          scriptPath,
          "--source-dir",
          sourceDir,
          "--output-dir",
          outputDir,
          "--recordings-base-url",
          "https://example.test/",
        ],
        {
          env: {
            ...process.env,
            CASSINI_VIEWER_DIST_DIR: distDir,
            PATH: `${stubBinDir}:${process.env.PATH}`,
          },
          encoding: "utf8",
        },
      );

      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      const byId = new Map(catalog.meetings.map((meeting: { id: string }) => [meeting.id, meeting]));
      // A date-only id is a recording-time claim and therefore outranks the
      // portable's createdAtUtc batch-processing timestamp.
      expect(byId.get("daily-meeting-2026-04-08")).toMatchObject({
        dateLabel: "2026-04-08",
      });
      // The same remains true when createdAtUtc is absent.
      expect(byId.get("daily-meeting-2026-04-09")).toMatchObject({
        dateLabel: "2026-04-09",
      });
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  // D-622: the room a recording came from must reach the catalog as its own
  // fields. The catalog title is not a usable substitute — it has the STT
  // variant appended, and nothing in it distinguishes "this room is called X"
  // from "we never learned the room name and fell back to the id".
  it("carries the room from the .opus into the catalog entry", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-export-room-"));
    try {
      const distDir = join(root, "dist");
      mkdirSync(distDir, { recursive: true });

      const stubBinDir = join(root, "bin");
      mkdirSync(stubBinDir, { recursive: true });
      const stubPath = join(stubBinDir, "ffprobe");
      writeFileSync(stubPath, '#!/bin/sh\nfor arg in "$@"; do last="$arg"; done\nexec cat "${last}.ffprobe.json"\n');
      chmodSync(stubPath, 0o755);

      const sourceDir = join(root, "source");
      mkdirSync(sourceDir, { recursive: true });
      writePublishedPortableProbeFixture(sourceDir, "01JZ8K3M4N5P6Q7R8S9T0VWXYZ", {
        id: "01JZ8K3M4N5P6Q7R8S9T0VWXYZ",
        title: "Weekly Sync",
        createdAtUtc: "2026-08-11T10:32:00Z",
        roomId: "rm_9f2a1c3d4e5b6a70",
      });
      writePublishedPortableProbeFixture(sourceDir, "01JZ8K3M4N5P6Q7R8S9T0VWXZZ", {
        id: "01JZ8K3M4N5P6Q7R8S9T0VWXZZ",
        title: "Cassini Meeting",
        createdAtUtc: "2026-08-12T10:32:00Z",
      });

      const outputDir = join(root, "out");
      execFileSync(
        process.execPath,
        [scriptPath, "--source-dir", sourceDir, "--output-dir", outputDir, "--recordings-base-url", "https://example.test/"],
        {
          env: { ...process.env, CASSINI_VIEWER_DIST_DIR: distDir, PATH: `${stubBinDir}:${process.env.PATH}` },
          encoding: "utf8",
        },
      );

      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      const byId = new Map(catalog.meetings.map((meeting: { id: string }) => [meeting.id, meeting]));
      expect(byId.get("01JZ8K3M4N5P6Q7R8S9T0VWXYZ")).toMatchObject({
        roomId: "rm_9f2a1c3d4e5b6a70",
      });
      // A meeting with no room ships no room keys at all, rather than two empty
      // strings a consumer would have to distinguish from a real value.
      const withoutRoom = byId.get("01JZ8K3M4N5P6Q7R8S9T0VWXZZ") as Record<string, unknown>;
      expect(withoutRoom).toBeDefined();
      expect("roomId" in withoutRoom).toBe(false);
      expect("roomName" in withoutRoom).toBe(false);
      // The version must be untouched: five unlinked readers check it for exact
      // equality, so the new fields have to be purely additive.
      expect(catalog.version).toBe("cassini.viewer.catalog.v1");
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  // D-716: the browse list and the Prepare panel size and gap-check a whole
  // selection. If those two facts are not in the catalog, answering "which of
  // these have a summary, and how many words is this selection" costs one
  // ranged fetch per meeting — which is the cost the catalog exists to avoid.
  it("carries summary presence and word count from the .opus into the catalog entry", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-export-content-"));
    try {
      const distDir = join(root, "dist");
      mkdirSync(distDir, { recursive: true });

      const stubBinDir = join(root, "bin");
      mkdirSync(stubBinDir, { recursive: true });
      const stubPath = join(stubBinDir, "ffprobe");
      writeFileSync(stubPath, '#!/bin/sh\nfor arg in "$@"; do last="$arg"; done\nexec cat "${last}.ffprobe.json"\n');
      chmodSync(stubPath, 0o755);

      const sourceDir = join(root, "source");
      mkdirSync(sourceDir, { recursive: true });
      writePublishedPortableProbeFixture(
        sourceDir,
        "01JZ8K3M4N5P6Q7R8S9T0VWXYZ",
        { id: "01JZ8K3M4N5P6Q7R8S9T0VWXYZ", title: "Weekly Sync", createdAtUtc: "2026-08-11T10:32:00Z" },
        { words: ["ship", "it", "today"], summaryMarkdown: "# Weekly Sync\n\n- Ship it.\n" },
      );
      writePublishedPortableProbeFixture(
        sourceDir,
        "01JZ8K3M4N5P6Q7R8S9T0VWXZZ",
        { id: "01JZ8K3M4N5P6Q7R8S9T0VWXZZ", title: "Weekly Sync", createdAtUtc: "2026-08-12T10:32:00Z" },
        { words: ["nobody", "summarised", "this", "one"] },
      );

      const outputDir = join(root, "out");
      execFileSync(
        process.execPath,
        [scriptPath, "--source-dir", sourceDir, "--output-dir", outputDir, "--recordings-base-url", "https://example.test/"],
        {
          env: { ...process.env, CASSINI_VIEWER_DIST_DIR: distDir, PATH: `${stubBinDir}:${process.env.PATH}` },
          encoding: "utf8",
        },
      );

      const catalog = JSON.parse(readFileSync(join(outputDir, "catalog.json"), "utf8"));
      const byId = new Map(catalog.meetings.map((meeting: { id: string }) => [meeting.id, meeting]));
      expect(byId.get("01JZ8K3M4N5P6Q7R8S9T0VWXYZ")).toMatchObject({
        hasSummary: true,
        wordCount: 3,
      });
      // Unlike the room fields, "no summary" IS shipped: the exporter opened
      // the file, so it can state the negative. Absence is reserved for an
      // archive published before the field existed.
      expect(byId.get("01JZ8K3M4N5P6Q7R8S9T0VWXZZ")).toMatchObject({
        hasSummary: false,
        wordCount: 4,
      });
      expect(catalog.version).toBe("cassini.viewer.catalog.v1");
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

describe("validatePublishedPortableManifest", () => {
  const manifest = {
    kind: "cassini-portable-meeting",
    version: 1,
    profile: "ogg-opus",
    integrity: {
      matchPolicy: "exact-opus-audio-v1",
      opusAudioSha256: "a".repeat(64),
    },
    transcripts: [{
      id: "raw-asr",
      role: "raw-asr",
      default: true,
      format: "transcript.words.v1",
      payloadRef: {
        prefix: "CASSINI_TX_RAW_ASR_PAYLOAD_",
        chunkCount: 1,
        sha256: "b".repeat(64),
        rawBytes: 2,
        gzipBytes: 22,
        mime: "application/vnd.cassini.transcript-words+json",
        encoding: "base64url+gzip+utf8json",
      },
    }],
  };

  it("accepts the published indexed manifest", () => {
    expect(() => validatePublishedPortableManifest(manifest)).not.toThrow();
  });

  it("accepts scripted words and an authoritative non-derived payload prefix", () => {
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{
        ...manifest.transcripts[0],
        id: "script",
        role: "scripted",
        payloadRef: {
          ...manifest.transcripts[0].payloadRef,
          prefix: "CASSINI_TX_AUTHORED_PAYLOAD_",
        },
      }],
    })).not.toThrow();
  });

  it("rejects an unsupported wire version", () => {
    expect(() => validatePublishedPortableManifest({ ...manifest, version: 0 })).toThrow(
      /unsupported portable manifest version/i,
    );
  });

  it("rejects malformed or unsupported transcript descriptors", () => {
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{ ...manifest.transcripts[0], role: "unknown" }],
    })).toThrow(/unsupported portable transcript role/i);
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{
        ...manifest.transcripts[0],
        payloadRef: { ...manifest.transcripts[0].payloadRef, encoding: "unknown" },
      }],
    })).toThrow(/unsupported transcript encoding/i);
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{ ...manifest.transcripts[0], id: "raw_asr" }],
    })).toThrow(/invalid portable transcript id/i);
  });

  it("enforces source relationships for every words role", () => {
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{ ...manifest.transcripts[0], sourceTranscriptId: "raw-asr" }],
    })).toThrow(/must not set sourceTranscriptId/i);
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [{ ...manifest.transcripts[0], id: "fixed", role: "human-corrected" }],
    })).toThrow(/unknown sourceTranscriptId/i);
    expect(() => validatePublishedPortableManifest({
      ...manifest,
      transcripts: [
        manifest.transcripts[0],
        {
          ...manifest.transcripts[0],
          id: "fixed",
          role: "human-corrected",
          sourceTranscriptId: "raw-asr",
          payloadRef: {
            ...manifest.transcripts[0].payloadRef,
            prefix: "CASSINI_TX_FIXED_PAYLOAD_",
          },
        },
      ],
    })).not.toThrow();
  });
});
