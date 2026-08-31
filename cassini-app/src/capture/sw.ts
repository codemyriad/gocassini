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

// The payload source, inlined at build time by scripts/build-capture.mjs.
//
// Inlined rather than fetched. A separate fetch could return 200 with a
// truncated body — a proxy hiccup, a half-written deploy — and a truncated
// payload appended to Talk's bundle is a syntax error in Talk's own script,
// which takes the call page down. There is no way to detect that reliably from
// inside the worker, so the fetch is removed instead of guarded.
declare const __CASSINI_PAYLOAD__: string;

// TALK_BUNDLE_SENTINEL is a marker every Talk bundle contains. It is the last
// check before rewriting: a URL that matches the path pattern but whose body is
// not Talk's script (an error page, a redirect landing, a proxy notice) must be
// passed through rather than have a payload welded onto it.
const TALK_BUNDLE_SENTINEL = "OCA";

// composeBundle appends the payload to Talk's bundle, separated by a newline
// and a semicolon so a bundle whose last line lacks a terminator cannot swallow
// the first payload statement. Pure, so the concatenation rule is unit-tested
// without a service worker environment.
export function composeBundle(talkSource: string, payloadSource: string): string {
  return `${talkSource}\n;\n/* cassini source-capture payload */\n${payloadSource}\n`;
}

// shouldRewrite decides whether a response is really Talk's script and safe to
// append to. Every condition here exists because getting it wrong corrupts
// somebody else's application:
//
//   - destination "script": the same URL fetched as a document, a prefetch or
//     an XHR is not being evaluated as Talk's bundle, and rewriting it would
//     hand the caller a body it did not ask for.
//   - status exactly 200: a 206 is a range, and appending to one fragment of a
//     script produces garbage. Redirects and errors are not ours to touch.
//   - a JavaScript content type: an error page served at the bundle's URL is
//     not something to weld a payload onto.
//   - the sentinel: last defence against a proxy notice or a login page that
//     satisfies everything above.
export function shouldRewrite(
  request: Request,
  response: Response,
  body: string,
  origin: string,
): boolean {
  if (request.headers.has("range")) {
    return false;
  }
  // Same-origin only. Talk's bundle is served by this Nextcloud; a
  // CORS-readable cross-origin URL that happens to match the path pattern is
  // somebody else's script and must not be rewritten.
  try {
    if (new URL(request.url).origin !== origin) {
      return false;
    }
  } catch {
    return false;
  }
  // Script destination only. The same URL fetched as an XHR, a prefetch or a
  // document is not being evaluated as Talk's bundle, and handing that caller
  // a body with our payload welded on is not what it asked for. An earlier
  // version also allowed "" for the convenience of a fetch()-based test; that
  // was the test dictating the policy, and the test now drives the real path
  // instead.
  if (request.destination !== "script") {
    return false;
  }
  if (response.status !== 200) {
    return false;
  }
  const type = (response.headers.get("content-type") ?? "").toLowerCase();
  if (!type.includes("javascript") && !type.includes("ecmascript")) {
    return false;
  }
  return body.includes(TALK_BUNDLE_SENTINEL);
}

// handleFetch returns the augmented bundle, or null when this request is none
// of our business (the caller then leaves the request entirely alone rather
// than proxying it through us for no reason).
export async function handleFetch(
  request: Request,
  fetchImpl: typeof fetch,
  payloadSource: string,
  origin: string,
): Promise<Response | null> {
  if (request.method !== "GET" || !isTalkBundleURL(request.url)) {
    return null;
  }
  const original = await fetchImpl(request);
  if (!original.ok) {
    return original;
  }
  // Read the body once, then decide: the sentinel check needs it, and a
  // Response body cannot be consumed twice.
  const talkSource = await original.clone().text();
  if (!shouldRewrite(request, original, talkSource, origin) || !payloadSource) {
    return original;
  }
  const headers = new Headers(original.headers);
  // The body length changed. A stale Content-Length truncates the script, and
  // a stale Content-Encoding makes the browser try to decompress plain text.
  headers.delete("content-length");
  headers.delete("content-encoding");
  // Validators and digests describe the ORIGINAL bytes; leaving them on a body
  // we changed invites a cache to serve one and revalidate against the other.
  headers.delete("etag");
  headers.delete("last-modified");
  headers.delete("digest");
  headers.delete("content-digest");
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
      handleFetch(event.request, fetch, __CASSINI_PAYLOAD__, scope.location.origin)
        .then((response) => response ?? fetch(event.request))
        .catch(() => fetch(event.request)),
    );
  });
}

if (typeof self !== "undefined" && typeof (self as Partial<ServiceWorkerGlobalScope>).skipWaiting === "function") {
  installListeners(self);
}
