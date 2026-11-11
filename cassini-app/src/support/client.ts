import { resolvePublishedUrl } from "cassini-viewer/dataProvider";
import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import { loadConfig } from "../operator/config";

type CatalogDiagnostic = {
  state: "not_requested" | "loading" | "loaded" | "failed";
  reason: "none" | "network_error" | "request_failed";
  detail?: string;
};

// Keep only the latest catalog failure, not response bodies or a console dump.
// Original error text is exported only when the user selects original diagnostics.
let catalog: CatalogDiagnostic = { state: "not_requested", reason: "none" };
export function recordCatalogDiagnostic(state: CatalogDiagnostic["state"], networkError = false, detail = ""): void {
  catalog = { state, reason: state === "failed" ? (networkError ? "network_error" : "request_failed") : "none", detail: state === "failed" ? detail.slice(0, 500) : undefined };
}

export function browserSupportContext(surface: "browse" | "operator", privacy: SupportPrivacy = "scrubbed") {
  return {
    surface,
    timezone_offset_minutes: new Date().getTimezoneOffset(),
    online: navigator.onLine,
    catalog_state: catalog.state,
    catalog_reason: catalog.reason,
    ...(privacy === "original" && catalog.detail ? { catalog_error: catalog.detail } : {}),
  };
}

export class SupportDownloadError extends Error {}
export type SupportPrivacy = "original" | "scrubbed";

export async function requestSupportBundle(
  administrator: boolean,
  surface: "browse" | "operator",
  signal: AbortSignal,
  privacy: SupportPrivacy = "original",
): Promise<{ blob: Blob; filename: string; administrator: boolean }> {
  const userURL = new URL("../support/bundle", resolvePublishedUrl("catalog.json")).href;
  const adminURL = administrator ? `${loadConfig().operatorBasePath.replace(/\/+$/, "")}/support-bundle` : userURL;
  const context = JSON.stringify({ ...browserSupportContext(surface, privacy), privacy });
  const request = (url: string) => fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { Accept: "application/zip", "Content-Type": "application/json" },
    body: context,
    signal,
  });
  let response = await request(administrator ? adminURL : userURL);
  // Rights can change after the shell's probe. The user endpoint still enforces
  // its own scope, and no flag from this request can widen it.
  if (administrator && response.status === 403) {
    administrator = false;
    response = await request(userURL);
  }
  if (!response.ok) {
    const t = get(LL).app.operator.support_bundle;
    const copy: Record<number, () => string> = {
      401: t.error_sign_in,
      403: t.error_forbidden,
      404: t.error_unavailable,
      429: t.error_busy,
    };
    throw new SupportDownloadError((copy[response.status] ?? t.error_failed)());
  }
  if (response.headers.get("Content-Type")?.split(";")[0] !== "application/zip") {
    throw new SupportDownloadError(get(LL).app.operator.support_bundle.error_not_a_bundle());
  }
  const blob = await response.blob();
  if (blob.size === 0 || blob.size > 21 * 1024 * 1024) {
    throw new SupportDownloadError(get(LL).app.operator.support_bundle.error_incomplete());
  }
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const filename = /filename="(cassini-support-(?:(?:original|scrubbed)-)?[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}\.zip)"/.exec(disposition)?.[1] ?? `cassini-support-${privacy}.zip`;
  return { blob, filename, administrator };
}
