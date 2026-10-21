import { resolvePublishedUrl, RoomNameError, type RoomNameState } from "cassini-viewer/dataProvider";

export async function requestRoomName(roomId: string, update?: { customName: string | null; expectRevision: number }): Promise<RoomNameState> {
  const url = new URL(`../rooms/${encodeURIComponent(roomId)}/name`, resolvePublishedUrl(""));
  let response: Response;
  try {
    response = await fetch(url, update ? {
      method: "POST", cache: "no-store", headers: { "Content-Type": "application/json" }, body: JSON.stringify(update),
    } : { cache: "no-store" });
  } catch {
    throw new RoomNameError("Could not reach Cassini. Your draft is still here.", null);
  }
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    throw new RoomNameError(response.status === 404 ? "This room is no longer available to you." : body?.error ?? "Could not update the room name.", response.status, body?.current);
  }
  if (!body || body.roomId !== roomId || typeof body.displayName !== "string" || typeof body.defaultName !== "string" || !(body.customName === null || typeof body.customName === "string") || !Number.isSafeInteger(body.revision) || body.revision < 1) {
    throw new RoomNameError("Cassini returned an invalid room name. Refresh before trying again.", null);
  }
  return body;
}
