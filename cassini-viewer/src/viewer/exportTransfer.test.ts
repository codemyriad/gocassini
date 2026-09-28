import { afterEach, describe, expect, it, vi } from "vitest";
import { loadAudioFile, zipAudioFiles } from "./exportTransfer";

afterEach(() => vi.restoreAllMocks());

describe("meeting file download", () => {
  it("fetches the caller-accessible file bytes and reports a denied download", async () => {
    const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(new Uint8Array([1, 2, 3]), { status: 200 }),
    ).mockResolvedValueOnce(new Response("denied", { status: 403 }));
    const entry = { id: "one", title: "One", audioPath: "/published/one.opus" };
    const file = await loadAudioFile(entry);
    expect(file.name).toBe("One.opus");
    expect(new Uint8Array(await file.blob.arrayBuffer())).toEqual(new Uint8Array([1, 2, 3]));
    expect(fetcher).toHaveBeenCalledWith(entry.audioPath, { cache: "no-store" });
    await expect(loadAudioFile(entry)).rejects.toThrow("(403)");
  });

  it("keeps the actual audio format for bundled and directory recordings", async () => {
    const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(new Uint8Array([4, 5]), { status: 200, headers: { "Content-Type": "audio/webm" } }),
    );
    const file = await loadAudioFile({ id: "one", title: "One", audioPath: "/meeting.webm" });
    expect(file.name).toBe("One.webm");
    expect(file.blob.type).toBe("audio/webm");
  });

  it("packages original audio bytes in a valid stored ZIP layout", async () => {
    const files = [
      { name: "one.opus", blob: new Blob([new Uint8Array([1, 2, 3])]) },
      { name: "two.opus", blob: new Blob([new Uint8Array([4, 5])]) },
    ];
    const blob = await zipAudioFiles(files);
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const view = new DataView(bytes.buffer);
    expect(view.getUint32(0, true)).toBe(0x04034b50);
    expect(view.getUint16(12, true)).toBe(0x0021);
    expect(view.getUint32(14, true)).toBe(0x55bc801d); // CRC-32 of 01 02 03
    const firstNameLength = view.getUint16(26, true);
    expect(new TextDecoder().decode(bytes.slice(30, 30 + firstNameLength))).toBe("one.opus");
    expect([...bytes.slice(30 + firstNameLength, 33 + firstNameLength)]).toEqual([1, 2, 3]);
    const end = bytes.length - 22;
    expect(view.getUint32(end, true)).toBe(0x06054b50);
    expect(view.getUint16(end + 10, true)).toBe(2);
    expect(view.getUint32(view.getUint32(end + 16, true), true)).toBe(0x02014b50);
  });

  it("keeps every recording when names collide after sanitization", async () => {
    const blob = await zipAudioFiles([
      { name: "same.opus", blob: new Blob([new Uint8Array([1])]) },
      { name: "same.opus", blob: new Blob([new Uint8Array([2])]) },
    ]);
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const names = new TextDecoder().decode(bytes);
    expect(names).toContain("same.opus");
    expect(names).toContain("same-2.opus");
  });
});
