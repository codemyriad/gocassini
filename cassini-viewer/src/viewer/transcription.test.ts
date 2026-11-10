import { readFileSync } from "node:fs";
import { describe, expect, it, vi, afterEach } from "vitest";
import { extractTranscriptionDocument } from "./portable";
import { StaticCatalogProvider } from "./dataProvider";
import { loadAudioFile } from "./exportTransfer";
import { lacksPortableAudio } from "./selectionModel";
import { portableRecordedAtUtc } from "../../scripts/meeting-start.mjs";

const fixture = () => JSON.parse(readFileSync(new URL("../../../spec/testdata/transcription-v1.json", import.meta.url), "utf8"));
afterEach(() => vi.unstubAllGlobals());
describe("transcription-only meetings", () => {
  it("reads the Go writer's complete document and rejects corrupt bodies", async () => {
    const doc = fixture();
    const {manifest} = await extractTranscriptionDocument(doc);
    expect(manifest.transcriptionOnly).toBe(true);
    expect(manifest.transcript).toEqual(doc.bodies[doc.source.transcripts[0].id]);
    const bad = fixture();
    bad.bodies[bad.source.transcripts[0].id].items[0].text = "tampered";
    await expect(extractTranscriptionDocument(bad)).rejects.toThrow("integrity mismatch");
  });
  // The document keeps the source file's tags; DATE among them is step 2 of
  // the format's "When the meeting happened", so it must reach the reader.
  it("keeps the source file's tags, so a DATE start is read as from the .opus", async () => {
    // The Go writer's fixture: DATE is createdAtUtc, the pack stamp.
    const stamped = await extractTranscriptionDocument(fixture());
    expect(stamped.tags.DATE).toBe(stamped.manifest.meeting?.createdAtUtc);
    expect(portableRecordedAtUtc(stamped.manifest, stamped.tags)).toBe("");
    const doc = fixture();
    doc.tags = {...doc.tags, DATE: "2026-10-06T19:00:00+02:00"};
    const {manifest, tags} = await extractTranscriptionDocument(doc);
    expect(portableRecordedAtUtc(manifest, tags)).toBe("2026-10-06T17:00:00.000Z");
  });
  it("loads, switches transcripts and downloads JSON without an audio source", async () => {
    vi.stubGlobal("window", {location: new URL("https://example.test/")});
    vi.stubGlobal("document", {baseURI: "https://example.test/", querySelector: () => null});
    const fetcher = vi.fn(async () => new Response(JSON.stringify(fixture()), {headers: {"Content-Type": "application/json"}}));
    vi.stubGlobal("fetch", fetcher);
    const entry = {id: "meeting", title: "Meeting", dateLabel: "2026-10-06", meetingPath: "https://example.test/meeting.json"};
    const provider = new StaticCatalogProvider();
    const loaded = await provider.loadMeetingForEntry(entry);
    expect(loaded.audioSrc).toBe("");
    expect(loaded.transcript.segments.length).toBeGreaterThan(0);
    expect((await provider.switchTranscript(entry, loaded.currentTranscriptId!)).audioSrc).toBe("");
    expect(lacksPortableAudio(entry)).toBe(false);
    expect((await loadAudioFile(entry)).name).toBe("Meeting.json");
    expect(fetcher.mock.calls.every(([url]) => url === entry.meetingPath)).toBe(true);
  });
});
