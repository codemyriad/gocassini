/// <reference lib="webworker" />
// Service worker that gets the capture payload onto Nextcloud Talk's call page.
//
// Why a service worker at all: Cassini ships as an AppAPI ExApp, which means no
// PHP runs inside Nextcloud, which means there is no supported way to add a
// script to another app's page. AppAPI's ui/script registration is applied only
// by its own TopMenuController, for type "top_menu", on AppAPI's embedded page;
// the navigation entry it adds elsewhere is a plain link with no JavaScript
// attached. A service worker is the one remaining same-origin mechanism.
//
// How it works: the operator serves this file with `Service-Worker-Allowed: /`,
// AppAPI's proxy forwards that header verbatim (ExAppProxyController::
// createProxyResponse copies every header except its own auth set), and the
// Cassini page registers us at the Talk call scopes. A service worker's scope
// decides which CLIENTS it controls, not which URLs it may intercept — so from
// a call page we see every subresource that page requests, Talk's own bundle
// included, and we serve that bundle with the capture payload appended.
//
// Read this before changing it:
//   - Appending to Talk's bundle rather than rewriting the page HTML avoids the
//     page CSP entirely. The bundle URL was already loaded by a nonce'd tag, so
//     the browser trusts the bytes we return; we never need the nonce.
//   - APPEND, never patch. Talk's internals change every release; appended code
//     that only touches web platform APIs survives upgrades that any textual
//     patch of Talk's source would not.
//   - Any failure must return Talk's real bundle. A broken capture feature is a
//     missing transcript improvement; a broken bundle is a Talk that will not
//     load, and it will look like Talk's bug to whoever debugs it.
//
// This is an unsupported mechanism: it modifies another app's JavaScript on the
// user's origin. It is appropriate for a deployment its operator controls, and
// it is not appropriate for a public app store listing — that wants the small
// PHP companion app doing the same job through Util::addScript. The capture and
// upload code is identical either way; only this file is thrown away.

import { isTalkBundleURL } from "./protocol";

declare const self: ServiceWorkerGlobalScope;

// PAYLOAD_FILENAME sits next to this script under the ExApp's /ui/ prefix, so
// the payload URL is derived from our own location rather than configured.
const PAYLOAD_FILENAME = "capture-payload.js";

// payloadURL resolves the payload next to the service worker script itself.
export function payloadURL(scriptURL: string): string {
  return new URL(PAYLOAD_FILENAME, scriptURL).toString();
}

// composeBundle appends the payload to Talk's bundle, separated by a newline
// and a semicolon so a bundle whose last line lacks a terminator cannot swallow
// the first payload statement. Pure, so the concatenation rule is unit-tested
// without a service worker environment.
export function composeBundle(talkSource: string, payloadSource: string): string {
  return `${talkSource}\n;\n/* cassini source-capture payload */\n${payloadSource}\n`;
}

// handleFetch returns the augmented bundle, or null when this request is none
// of our business (the caller then leaves the request entirely alone rather
// than proxying it through us for no reason).
export async function handleFetch(
  request: Request,
  fetchImpl: typeof fetch,
  scriptURL: string,
): Promise<Response | null> {
  if (request.method !== "GET" || !isTalkBundleURL(request.url)) {
    return null;
  }
  const original = await fetchImpl(request);
  if (!original.ok) {
    return original;
  }
  let payloadSource: string;
  try {
    const payload = await fetchImpl(payloadURL(scriptURL), { credentials: "same-origin" });
    if (!payload.ok) {
      return original;
    }
    payloadSource = await payload.text();
  } catch {
    // Offline, proxy hiccup, ExApp down: Talk must still load.
    return original;
  }
  const talkSource = await original.text();
  const headers = new Headers(original.headers);
  // The body length changed, and a stale Content-Length truncates the script.
  headers.delete("content-length");
  headers.delete("content-encoding");
  return new Response(composeBundle(talkSource, payloadSource), {
    status: original.status,
    statusText: original.statusText,
    headers,
  });
}

// installListeners wires the worker lifecycle. Split from the module body so a
// unit test can import handleFetch in Node, where no worker global exists.
export function installListeners(scope: ServiceWorkerGlobalScope): void {
  scope.addEventListener("install", (event) => {
    // Take over as soon as the new version is installed: the alternative is a
    // payload that only starts working after the user closes every Talk tab.
    event.waitUntil(scope.skipWaiting());
  });

  scope.addEventListener("activate", (event) => {
    event.waitUntil(scope.clients.claim());
  });

  scope.addEventListener("fetch", (event) => {
    // Decide synchronously and do not call respondWith for anything else: a
    // service worker that answers every request routes the whole call page's
    // traffic through this thread for no reason. Declining leaves the request on
    // the browser's own path.
    if (event.request.method !== "GET" || !isTalkBundleURL(event.request.url)) {
      return;
    }
    event.respondWith(
      handleFetch(event.request, fetch, scope.location.href)
        .then((response) => response ?? fetch(event.request))
        .catch(() => fetch(event.request)),
    );
  });
}

if (typeof self !== "undefined" && typeof (self as Partial<ServiceWorkerGlobalScope>).skipWaiting === "function") {
  installListeners(self);
}
