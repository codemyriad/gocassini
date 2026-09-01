// Per-browser consent and retirement of PR #228's legacy service worker.
//
// Capture delivery now belongs to the cassini_capture companion app. The
// Cassini ExApp page only records a participant's explicit opt-in. It never
// registers a worker or claims a Talk URL scope.

export const CONSENT_STORAGE_KEY = "cassini.sourceCapture.consent";

const LEGACY_CAPTURE_WORKER_SUFFIX = "/apps/app_api/proxy/gocassini/ui/capture-sw.js";

export function isEnabled(storage: Pick<Storage, "getItem">): boolean {
  try {
    return storage.getItem(CONSENT_STORAGE_KEY) === "granted";
  } catch {
    return false;
  }
}

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
