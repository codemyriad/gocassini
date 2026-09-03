// Retirement of PR #228's legacy service worker.
//
// Capture delivery now belongs to the cassini_capture companion app. The
// Cassini ExApp page holds no capture state of its own: it never registers a
// worker, claims a Talk URL scope, or stores anything per participant.

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
