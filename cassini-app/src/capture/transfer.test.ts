import { describe, expect, it, vi } from "vitest";
import { CAPTURE_PIECE_BYTES, hashCapturePiece, transferCapture } from "./transfer";
import { SOURCE_CAPTURE_FORMAT, type CaptureSidecar } from "./protocol";

const sidecar: CaptureSidecar = {
  format: SOURCE_CAPTURE_FORMAT, roomToken: "room", participantId: "alice",
  recordingId: "recording", sessionId: "session", callStartWallMs: 1, callEndWallMs: 2,
  userAgent: "test", segments: [{ index: 0, audioName: "segment-0.webm",
    mimeType: "audio/webm", startWallMs: 1, stopWallMs: 2, sampleRate: 48000,
    channelCount: 1, anchors: [], muteIntervals: [] }],
};

describe("immutable transfer", () => {
  it("retries only missing pieces after a lost acknowledgement", async () => {
    const first = new Blob([new Uint8Array(CAPTURE_PIECE_BYTES)]);
    const file = new Blob([first, "tail"]);
    const firstHash = await hashCapturePiece(first);
    const sent: string[] = [];
    const fetchImpl = vi.fn(async (url: string, init: RequestInit) => {
      if (!init.method) return Response.json({ pieces: [firstHash] });
      sent.push(url);
      return new Response(null, { status: 204 });
    });
    await transferCapture("/proxy", sidecar, async () => file, () => true, "token", fetchImpl as never);
    expect(sent).toHaveLength(2);
    expect(sent[0]).toContain("piece=" + await hashCapturePiece(new Blob(["tail"])));
    expect(sent[1]).toContain("op=commit");
  });
  it("verifies the manifest without retransmitting a committed session", async () => {
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) =>
      !init.method ? Response.json({ committed: true }) : new Response(null, { status: 204 }));
    await transferCapture("/proxy", sidecar, async () => new Blob(["audio"]), () => true, "token", fetchImpl as never);
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    expect(fetchImpl.mock.calls[1][0]).toContain("op=commit");
  });
  it("stops before sending another piece when permission is revoked", async () => {
    let allowed = true;
    const fetchImpl = vi.fn(async () => { allowed = false; return Response.json({ pieces: [] }); });
    await expect(transferCapture("/proxy", sidecar, async () => new Blob(["audio"]), () => allowed, "token", fetchImpl as never)).rejects.toThrow("revoked");
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });
  it("leaves a rejected manifest retryable", async () => {
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) =>
      !init.method ? Response.json({ committed: true }) : new Response(null, { status: 409 }));
    await expect(transferCapture("/proxy", sidecar, async () => new Blob(["audio"]), () => true, "token", fetchImpl as never)).rejects.toThrow("409");
  });
});
