import { readFileSync } from "node:fs";
import { describe, expect, it, vi, afterEach } from "vitest";
import { extractTranscriptionDocument } from "./portable";
import { StaticCatalogProvider } from "./dataProvider";
import { loadAudioFile } from "./exportTransfer";
import { lacksPortableAudio } from "./selectionModel";

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
