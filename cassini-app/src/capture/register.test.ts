import { describe, expect, it, vi } from "vitest";
import { retireLegacyCaptureWorkers } from "./register";

describe("retireLegacyCaptureWorkers", () => {
  it("removes only Cassini's obsolete worker by script URL", async () => {
    const legacy = vi.fn(async () => true);
    const files = vi.fn(async () => true);
    const talk = vi.fn(async () => true);
    const unrelatedCaptureName = vi.fn(async () => true);
    const container = {
      getRegistrations: vi.fn(async () => [
        {
          scope: "https://cloud.example.com/call/",
          active: {
            scriptURL:
              "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/ui/capture-sw.js",
          },
          unregister: legacy,
        },
        {
          scope: "https://cloud.example.com/",
          active: { scriptURL: "https://cloud.example.com/apps/files/preview-service-worker.js" },
          unregister: files,
        },
        {
          scope: "https://cloud.example.com/call/",
          active: { scriptURL: "https://cloud.example.com/apps/spreed/js/talk-worker.js" },
          unregister: talk,
        },
        {
          scope: "https://cloud.example.com/another/",
          active: {
            scriptURL:
              "https://cloud.example.com/index.php/apps/app_api/proxy/another-app/ui/capture-sw.js",
          },
          unregister: unrelatedCaptureName,
        },
        {
          scope: "https://cloud.example.com/index.php/call/",
          active: { scriptURL: "https://cloud.example.com/apps/spreed/js/talk-worker.js" },
          waiting: {
            scriptURL:
              "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/ui/capture-sw.js",
          },
          unregister: legacy,
        },
      ]),
    } as unknown as ServiceWorkerContainer;

    await expect(retireLegacyCaptureWorkers(container)).resolves.toBe(2);
    expect(legacy).toHaveBeenCalledTimes(2);
    expect(files).not.toHaveBeenCalled();
    expect(talk).not.toHaveBeenCalled();
    expect(unrelatedCaptureName).not.toHaveBeenCalled();
  });

  it("is harmless without service-worker support", async () => {
    await expect(retireLegacyCaptureWorkers(undefined)).resolves.toBe(0);
  });
});
