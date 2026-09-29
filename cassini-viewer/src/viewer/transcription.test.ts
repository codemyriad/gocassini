import { readFileSync } from "node:fs";
import { describe, expect, it, vi, afterEach } from "vitest";
import { extractPortableManifestFromArrayBuffer, loadPortableTranscriptBody } from "./portable";
import { loadPortableArtifactFromAudioPath, PortableMeetingStore, switchPortableTranscript } from "./loadArtifact";

const fixture = readFileSync(new URL("../../../spec/fixtures/retained-meeting.json", import.meta.url));
afterEach(() => vi.unstubAllGlobals());

describe("portable retained meetings", () => {
  it("reads the shared Go fixture and every language without an audio source", async () => {
    const result = await extractPortableManifestFromArrayBuffer(fixture);
    expect(result.mediaState).toBe("evicted");
    expect(result.manifest.transcripts).toHaveLength(2);
    const spanish = result.manifest.transcripts!.find((entry) => entry.id === "spanish")!;
    const body = await loadPortableTranscriptBody(result.tags, spanish.payloadRef) as { items: { text: string }[] };
    expect(body.items.map((item) => item.text)).toEqual(["Hola", "mundo"]);
    const fetch = vi.fn(async () => new Response(fixture));
    vi.stubGlobal("fetch", fetch);
    vi.stubGlobal("window", { location: new URL("http://localhost/") });
    const store = new PortableMeetingStore();
    const loaded = await loadPortableArtifactFromAudioPath("http://localhost/meeting.opus", store);
    expect(loaded.audioSrc).toBe("");
    expect(loaded.mediaState).toBe("evicted");
    const switched = await switchPortableTranscript("http://localhost/meeting.opus", "spanish", store);
    expect(switched.audioSrc).toBe("");
    expect(switched.mediaState).toBe("evicted");
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it("rejects a damaged secondary payload and mismatched identity", async () => {
    for (const mutate of [
      (doc: any) => { const key = Object.keys(doc.current.payloads)[1]; doc.current.payloads[key].dataBase64 = "e30="; },
      (doc: any) => { doc.identity.originalAudioSha256 = "0".repeat(64); },
      (doc: any) => { doc.media.state = "available"; },
    ]) {
      const doc = JSON.parse(fixture.toString()); mutate(doc);
      await expect(extractPortableManifestFromArrayBuffer(new TextEncoder().encode(JSON.stringify(doc)))).rejects.toThrow();
    }
  });
});

it("rejects duplicate keys and excessive extension nesting", async () => {
  const duplicate = fixture.toString().replace('"format":"cassini.transcription.v1"', '"format":"cassini.transcription.v1","format":"cassini.transcription.v1"');
  expect(duplicate).not.toBe(fixture.toString());
  await expect(extractPortableManifestFromArrayBuffer(new TextEncoder().encode(duplicate))).rejects.toThrow(/Duplicate/);
  const document = JSON.parse(fixture.toString());
  document.futureDepth = JSON.parse('['.repeat(70) + '0' + ']'.repeat(70));
  await expect(extractPortableManifestFromArrayBuffer(new TextEncoder().encode(JSON.stringify(document)))).rejects.toThrow(/nesting/);
});
