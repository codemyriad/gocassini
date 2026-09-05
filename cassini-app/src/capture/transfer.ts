import type { CaptureSidecar } from "./protocol";

export const CAPTURE_PIECE_BYTES = 4 * 1024 * 1024;
export async function hashCapturePiece(blob: Blob): Promise<string> {
  const hash = await crypto.subtle.digest("SHA-256", await blob.arrayBuffer());
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

// Inventory makes retries independent of local acknowledgement bookkeeping.
export async function transferCapture(base: string, sidecar: CaptureSidecar,
  readFile: (name: string) => Promise<Blob>, allowed: () => boolean,
  requestToken: string, fetchImpl: typeof fetch = fetch): Promise<void> {
  if (!sidecar.recordingId || !sidecar.sessionId) throw new Error("capture has no recording identity");
  const query = new URLSearchParams({ room: sidecar.roomToken, recording: sidecar.recordingId, session: sidecar.sessionId });
  const url = base.replace(/\/$/, "") + "/operator/capture/transfer?" + query;
  const request = async (target: string, init: RequestInit = {}): Promise<Response> => {
    if (!allowed()) throw new Error("capture permission revoked");
    const response = await fetchImpl(target, {
      ...init, credentials: "same-origin", cache: "no-store",
      headers: { requesttoken: requestToken, ...init.headers },
      signal: AbortSignal.timeout(60_000),
    });
    if (!response.ok) throw new Error("capture transfer deferred: " + response.status);
    return response;
  };
  const inventory = await (await request(url)).json() as { pieces?: string[]; committed?: boolean };
  const known = new Set(inventory.pieces ?? []);
  const pieces: Record<string, string[]> = {};
  for (const segment of sidecar.segments) {
    const file = await readFile(segment.audioName);
    const hashes: string[] = [];
    for (let offset = 0; offset < file.size; offset += CAPTURE_PIECE_BYTES) {
      const piece = file.slice(offset, offset + CAPTURE_PIECE_BYTES);
      const hash = await hashCapturePiece(piece);
      hashes.push(hash);
      if (!inventory.committed && !known.has(hash)) {
        const form = new FormData();
        form.append("piece", piece, hash + ".part");
        await request(url + "&piece=" + hash, { method: "POST", body: form });
        known.add(hash);
      }
    }
    pieces[segment.audioName] = hashes;
  }
  await request(url + "&op=commit", {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sidecar, pieces }),
  });
}
