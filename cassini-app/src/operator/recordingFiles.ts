import { insightFilesUrl } from "../insights/filesUrl";
import { nextcloudRootPath, nextcloudUrl } from "./ncSetup";

// Where a run's recording opens in Nextcloud Files, for the administrator
// looking at it (D-825).
//
// The recording belongs to the `cassini` account and is shared with its room's
// audience, so an administrator can open it only if they were in that audience
// (or are `cassini`). The operator knows the file id but not who the viewer is
// in Nextcloud's eyes — group and Team membership included — so it does not
// decide. The page asks Nextcloud, as the signed-in administrator, to find the
// file id in their own Files: a hit is the path the file has for them, and the
// link goes there; no hit means no link, never a link to a "not found".

interface CurrentUserGlobals {
  getCurrentUser?: () => { uid?: string | null } | null;
  requestToken?: string;
}

function currentUser(): CurrentUserGlobals | null {
  const oc = (globalThis as { OC?: CurrentUserGlobals }).OC;
  return oc && typeof oc === "object" ? oc : null;
}

function escapeXML(value: string): string {
  return value.replace(/[<>&'"]/g, (c) => `&#${c.charCodeAt(0)};`);
}

// searchBody is a DAV SEARCH for the one file with this id anywhere in the
// user's Files, shares included. `oc:fileid` is a searchable property of
// Nextcloud's file search backend.
export function searchBody(uid: string, fileId: number): string {
  return `<?xml version="1.0" encoding="UTF-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:basicsearch>
    <d:select><d:prop><oc:fileid/></d:prop></d:select>
    <d:from><d:scope><d:href>/files/${escapeXML(uid)}</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where><d:eq><d:prop><oc:fileid/></d:prop><d:literal>${fileId}</d:literal></d:eq></d:where>
    <d:orderby/>
  </d:basicsearch>
</d:searchrequest>`;
}

// pathFromMultistatus is the first result's path relative to the user's Files
// root, or "" when there is none. The response names each file by its DAV
// href, `<root>/remote.php/dav/files/<uid>/<path>`, every segment encoded.
export function pathFromMultistatus(xml: string, uid: string): string {
  const match = /<(?:[\w-]+:)?href>([^<]+)<\/(?:[\w-]+:)?href>/i.exec(xml);
  if (!match) {
    return "";
  }
  const home = `/remote.php/dav/files/${encodeURIComponent(uid)}/`;
  const href = match[1].trim();
  const at = href.indexOf(home);
  if (at < 0) {
    return "";
  }
  try {
    return href
      .slice(at + home.length)
      .split("/")
      .map((segment) => decodeURIComponent(segment))
      .join("/");
  } catch {
    return "";
  }
}

// findRecordingInFiles is the recording's path in the signed-in user's Files,
// or "" when they cannot open it. A failed request is logged and treated as
// "cannot": the link is a convenience, and the run detail renders without it.
export async function findRecordingInFiles(fileId: number, fetchImpl: typeof fetch = fetch): Promise<string> {
  const oc = currentUser();
  const uid = oc?.getCurrentUser?.()?.uid ?? "";
  if (!Number.isSafeInteger(fileId) || fileId <= 0 || uid === "") {
    return "";
  }
  try {
    const response = await fetchImpl(nextcloudUrl("/remote.php/dav/"), {
      method: "SEARCH",
      credentials: "same-origin",
      headers: { "Content-Type": "text/xml; charset=utf-8", requesttoken: oc?.requestToken ?? "" },
      body: searchBody(uid, fileId),
    });
    if (response.status !== 207) {
      console.warn(`Cassini: looking up recording ${fileId} in Files answered HTTP ${response.status}.`);
      return "";
    }
    return pathFromMultistatus(await response.text(), uid);
  } catch (error) {
    console.warn(`Cassini: looking up recording ${fileId} in Files failed.`, error);
    return "";
  }
}

// recordingFilesUrl opens Files on the recording, with its details sidebar —
// where its shares are — open.
export function recordingFilesUrl(location: Pick<Location, "origin" | "pathname">, path: string, fileId: number): string {
  return insightFilesUrl(location, path, nextcloudRootPath(), String(fileId));
}
