import { describe, expect, it, vi } from "vitest";
import { loadAudioFile, zipAudioFiles } from "./exportTransfer";

describe("meeting file download", () => {
  it("fetches the caller-accessible file bytes and reports a denied download", async () => {
    const fetcher = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(new Uint8Array([1, 2, 3]), { status: 200 }),
    ).mockResolvedValueOnce(new Response("denied", { status: 403 }));
    const entry = { id: "one", title: "One", audioPath: "/published/one.opus" };
    expect(await loadAudioFile(entry)).toEqual({ name: "One-one.opus", bytes: new Uint8Array([1, 2, 3]) });
    expect(fetcher).toHaveBeenCalledWith(entry.audioPath, { cache: "no-store" });
    await expect(loadAudioFile(entry)).rejects.toThrow("(403)");
    fetcher.mockRestore();
  });

  it("packages original audio bytes in a valid stored ZIP layout", async () => {
    const files = [
      { name: "one.opus", bytes: new Uint8Array([1, 2, 3]) },
      { name: "two.opus", bytes: new Uint8Array([4, 5]) },
    ];
    const blob = zipAudioFiles(files);
    const bytes = new Uint8Array(await blob.arrayBuffer());
    const view = new DataView(bytes.buffer);
    expect(view.getUint32(0, true)).toBe(0x04034b50);
    expect(view.getUint32(14, true)).toBe(0x55bc801d); // CRC-32 of 01 02 03
    const firstNameLength = view.getUint16(26, true);
    expect(new TextDecoder().decode(bytes.slice(30, 30 + firstNameLength))).toBe("one.opus");
    expect([...bytes.slice(30 + firstNameLength, 33 + firstNameLength)]).toEqual([1, 2, 3]);
    const end = bytes.length - 22;
    expect(view.getUint32(end, true)).toBe(0x06054b50);
    expect(view.getUint16(end + 10, true)).toBe(2);
    expect(view.getUint32(view.getUint32(end + 16, true), true)).toBe(0x02014b50);
  });
});
