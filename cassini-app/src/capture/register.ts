// Retirement of what earlier builds of source capture left in a browser.
//
// Capture delivery now belongs to the cassini_capture companion app, and
// capture itself follows Talk's official recording. The Cassini ExApp page
// holds no capture state of its own: it never registers a worker, claims a Talk
// URL scope, or stores anything per participant.

// LEGACY_CONSENT_STORAGE_KEY is the per-browser opt-in this feature used to
// keep. Nothing reads or writes it any more, which is exactly why it has to be
// deleted rather than ignored: a value sitting here is a recorded answer to a
// question this build no longer asks, on a profile whose owner may never touch
// Cassini again, and no such answer is kept.
const LEGACY_CONSENT_STORAGE_KEY = "cassini.sourceCapture.consent";

// forgetLegacyConsent removes it, and only it.
//
// cassini.sourceCapture.uploadAttempts is deliberately untouched: that counts
// delivery refusals per buffered capture so a permanently-failing deployment
// stops re-offering a meeting-sized body forever, and it says nothing about a
// person. localStorage is read off globalThis inside the try because this runs
// in a Talk call page, a Cassini page, and a unit test, and a storage that
// throws or is absent must not break any of them.
export function forgetLegacyConsent(): void {
  try {
    (globalThis as { localStorage?: Pick<Storage, "removeItem"> }).localStorage?.removeItem(
      LEGACY_CONSENT_STORAGE_KEY,
    );
  } catch {
    // Private mode, disabled or full storage. There is nothing to fall back on,
    // and nothing here is worth degrading a live call over.
  }
}

const LEGACY_CAPTURE_WORKER_SUFFIX = "/apps/app_api/proxy/gocassini/ui/capture-sw.js";

function legacyCaptureWorker(registration: ServiceWorkerRegistration): boolean {
  return [registration.active, registration.waiting, registration.installing].some((worker) => {
    if (!worker?.scriptURL) {
      return false;
    }
    try {
      return new URL(worker.scriptURL).pathname.endsWith(LEGACY_CAPTURE_WORKER_SUFFIX);
    } catch {
      return false;
    }
  });
}

// Existing installs may still have the bundle-rewriting worker from the draft
// prototype. Removing its route does not remove an installed worker, so both
// the Cassini page and the new Talk payload call this migration helper. Match
// the script URL, never the scope: a Talk- or Files-owned worker at the same
// scope must survive.
export async function retireLegacyCaptureWorkers(
  container: Pick<ServiceWorkerContainer, "getRegistrations"> | undefined,
): Promise<number> {
  if (!container) {
    return 0;
  }
  const registrations = await container.getRegistrations();
  let removed = 0;
  for (const registration of registrations) {
    if (legacyCaptureWorker(registration) && (await registration.unregister())) {
      removed += 1;
    }
  }
  return removed;
}
