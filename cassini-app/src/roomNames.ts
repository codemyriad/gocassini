import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import { resolvePublishedUrl, RoomNameError, type RoomNameState } from "cassini-viewer/dataProvider";

export async function requestRoomName(roomId: string, update?: { customName: string | null; expectRevision: number }): Promise<RoomNameState> {
  const url = new URL(`../rooms/${encodeURIComponent(roomId)}/name`, resolvePublishedUrl(""));
  let response: Response;
  try {
    response = await fetch(url, update ? {
      method: "POST", cache: "no-store", headers: { "Content-Type": "application/json" }, body: JSON.stringify(update),
    } : { cache: "no-store" });
  } catch {
    throw new RoomNameError(get(LL).app.shell.room_names.unreachable(), null);
  }
  const body = await response.json().catch(() => null);
  if (!response.ok) {
    // body.error is the server's own sentence, shown as it was sent.
    const t = get(LL).app.shell.room_names;
    throw new RoomNameError(response.status === 404 ? t.room_gone() : body?.error ?? t.update_failed(), response.status, body?.current);
  }
  if (!body || body.roomId !== roomId || typeof body.displayName !== "string" || typeof body.defaultName !== "string" || !(body.customName === null || typeof body.customName === "string") || !Number.isSafeInteger(body.revision) || body.revision < 1) {
    throw new RoomNameError(get(LL).app.shell.room_names.invalid_response(), null);
  }
  return body;
}
