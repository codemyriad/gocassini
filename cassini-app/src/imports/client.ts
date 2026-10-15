// The import client (D-828): staging into the caller's own Files, the operator
// imports API, and Nextcloud's sharee search.
//
// Staging deliberately does NOT go through the operator. The browser PUTs the
// file over Nextcloud's own WebDAV into `.CassiniImports/` in the caller's
// Files — same origin, session cookie plus requesttoken, the transport the
// Files app itself uses — so the operator API keeps its small-JSON-only
// invariant and the large transfer rides infrastructure already sized for it.
// The operator later pulls the staged file AS THE CALLER and removes it after
// a successful import.

import { resolvePublishedUrl } from "cassini-viewer/dataProvider";
import type { ImportFileRequest, ImportJobRecord, ImportSharee } from "cassini-viewer/imports";

// The staging folder inside the caller's Files. Must match the operator's
// importStagingDir — it refuses staged paths anywhere else.
const STAGING_DIR = ".CassiniImports";

interface NextcloudWindowGlobals {
  requestToken?: string;
  currentUser?: string;
  getRootPath?: () => string;
  getCurrentUser?: () => { uid?: string } | null;
}

function nextcloudGlobals(): NextcloudWindowGlobals | null {
  const oc = (globalThis as { OC?: NextcloudWindowGlobals }).OC;
  return oc && typeof oc === "object" ? oc : null;
}

function nextcloudOriginUrl(path: string): string {
  const root = nextcloudGlobals()?.getRootPath?.() ?? "";
  return `${root.replace(/\/+$/, "")}${path}`;
}

function currentUserId(): string {
  const oc = nextcloudGlobals();
  const uid = oc?.getCurrentUser?.()?.uid ?? oc?.currentUser ?? "";
  if (!uid) {
    throw new Error("Import needs a Nextcloud session, and this page has none.");
  }
  return uid;
}

function requestToken(): string {
  return nextcloudGlobals()?.requestToken ?? "";
}

// --- staging over WebDAV -------------------------------------------------------

function davBase(): string {
  return nextcloudOriginUrl(`/remote.php/dav/files/${encodeURIComponent(currentUserId())}`);
}

// A staged name that cannot collide across files or tabs, keeping enough of the
// original for the folder to stay legible to its owner.
function stagedName(fileName: string): string {
  const safe = fileName.replace(/[\\/\u0000-\u001f]/g, "_").slice(-120) || "meeting";
  const nonce = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
  return `${nonce}-${safe}`;
}

async function ensureStagingDir(): Promise<void> {
  const response = await fetch(`${davBase()}/${encodeURIComponent(STAGING_DIR)}`, {
    method: "MKCOL",
    credentials: "same-origin",
    headers: { requesttoken: requestToken() },
  });
  // 405 is "it already exists" — the normal case after the first import.
  if (!response.ok && response.status !== 405) {
    throw new Error(`Could not prepare the staging folder (HTTP ${response.status}).`);
  }
}

// stageImportUpload PUTs one file and resolves to the path the operator is
// given. XMLHttpRequest rather than fetch for the one thing fetch still cannot
// do: upload progress events for the per-file bar.
export async function stageImportUpload(
  file: File,
  onProgress?: (sentBytes: number, totalBytes: number) => void,
): Promise<string> {
  await ensureStagingDir();
  const name = stagedName(file.name);
  const url = `${davBase()}/${encodeURIComponent(STAGING_DIR)}/${encodeURIComponent(name)}`;
  await new Promise<void>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("PUT", url);
    xhr.withCredentials = true;
    xhr.setRequestHeader("requesttoken", requestToken());
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        onProgress?.(event.loaded, event.total);
      }
    };
    xhr.onerror = () => reject(new Error("The upload failed — check the connection and try again."));
    xhr.onabort = () => reject(new Error("The upload was interrupted."));
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress?.(file.size, file.size);
        resolve();
      } else if (xhr.status === 507) {
        reject(new Error("Not enough space in your Nextcloud Files for this upload."));
      } else {
        reject(new Error(`The upload was refused (HTTP ${xhr.status}).`));
      }
    };
    xhr.send(file);
  });
  return `${STAGING_DIR}/${name}`;
}

// --- the operator imports API ---------------------------------------------------

// A sibling of the published archive, exactly as the annotations routes are.
function importsUrl(path = ""): string {
  return new URL(`../imports${path}`, resolvePublishedUrl("")).toString();
}

async function importsRequest<T>(path: string, body?: object): Promise<T> {
  const init: RequestInit =
    body === undefined
      ? { headers: { Accept: "application/json" } }
      : {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify(body),
        };
  // no-store: AppAPI caches a proxied GET for an hour, and this surface is
  // polled for progress.
  const response = await fetch(importsUrl(path), { ...init, cache: "no-store" });
  if (!response.ok) {
    throw new Error(await describeImportFailure(response));
  }
  return (await response.json()) as T;
}

export async function startImports(files: ImportFileRequest[]): Promise<ImportJobRecord[]> {
  const result = await importsRequest<{ imports: ImportJobRecord[] }>("", { files });
  return result.imports;
}

export async function listImports(): Promise<ImportJobRecord[]> {
  return (await importsRequest<{ imports: ImportJobRecord[] }>("")).imports;
}

export async function cancelImport(id: string): Promise<void> {
  await importsRequest(`/${encodeURIComponent(id)}/cancel`, {});
}

export async function retryImport(id: string): Promise<void> {
  await importsRequest(`/${encodeURIComponent(id)}/retry`, {});
}

async function describeImportFailure(response: Response): Promise<string> {
  let served = "";
  try {
    const payload = JSON.parse(await response.text()) as { error?: unknown };
    if (typeof payload.error === "string") served = payload.error.trim();
  } catch {
    // Not JSON: the status is all there is.
  }
  if (served) return served;
  switch (response.status) {
    case 404:
      return "This deployment does not serve meeting imports.";
    case 502:
      return "Cassini could not verify who you are; reload and try again.";
    default:
      return `The import request failed (HTTP ${response.status}).`;
  }
}

// --- sharee search ----------------------------------------------------------------

// Nextcloud's own sharee search, narrowed to the kinds recording shares accept.
// Asked directly of Nextcloud (same origin, OCS) rather than proxied: the
// catalogue of who can be shared with is Nextcloud's to answer, and the session
// on this page is already the right identity.
interface OcsShareeEntry {
  label?: string;
  value?: { shareType?: number; shareWith?: string };
}

interface OcsShareesPayload {
  ocs?: {
    data?: {
      exact?: { users?: OcsShareeEntry[]; groups?: OcsShareeEntry[]; circles?: OcsShareeEntry[] };
      users?: OcsShareeEntry[];
      groups?: OcsShareeEntry[];
      circles?: OcsShareeEntry[];
    };
  };
}

export async function searchImportSharees(query: string): Promise<ImportSharee[]> {
  const trimmed = query.trim();
  if (trimmed === "") return [];
  const url = new URL(nextcloudOriginUrl("/ocs/v2.php/apps/files_sharing/api/v1/sharees"), window.location.origin);
  url.searchParams.set("search", trimmed);
  url.searchParams.set("itemType", "file");
  url.searchParams.set("perPage", "20");
  url.searchParams.set("format", "json");
  const response = await fetch(url.toString(), {
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      "OCS-APIRequest": "true",
      requesttoken: requestToken(),
    },
  });
  if (!response.ok) {
    throw new Error(`People search failed (HTTP ${response.status}).`);
  }
  const payload = (await response.json()) as OcsShareesPayload;
  const data = payload.ocs?.data;
  const sharees: ImportSharee[] = [];
  const seen = new Set<string>();
  const take = (entries: OcsShareeEntry[] | undefined, type: ImportSharee["type"]) => {
    for (const entry of entries ?? []) {
      const id = String(entry.value?.shareWith ?? "").trim();
      if (!id) continue;
      const key = `${type}\u0000${id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      sharees.push({ type, id, label: String(entry.label ?? "").trim() || id });
    }
  };
  take(data?.exact?.users, "user");
  take(data?.users, "user");
  take(data?.exact?.groups, "group");
  take(data?.groups, "group");
  take(data?.exact?.circles, "circle");
  take(data?.circles, "circle");
  return sharees;
}
