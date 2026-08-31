import { describe, expect, it, vi } from "vitest";
import { isEnabled, registerAll, serviceWorkerURL, setUp, unregisterAll } from "./register";

const PROXY_BASE = "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/";

function fakeContainer() {
  const registered: Array<{ url: string; scope: string }> = [];
  const unregistered: string[] = [];
  return {
    registered,
    unregistered,
    container: {
      register: vi.fn(async (url: string, options?: { scope?: string }) => {
        registered.push({ url, scope: options?.scope ?? "" });
        return {} as ServiceWorkerRegistration;
      }),
      getRegistrations: vi.fn(async () => [
        { scope: "https://cloud.example.com/call/", unregister: async () => void unregistered.push("/call/") },
        { scope: "https://cloud.example.com/", unregister: async () => void unregistered.push("/") },
      ]),
    } as unknown as ServiceWorkerContainer,
  };
}

describe("serviceWorkerURL", () => {
  it("points at the operator asset served with Service-Worker-Allowed", () => {
    expect(serviceWorkerURL(PROXY_BASE)).toBe(PROXY_BASE + "ui/capture-sw.js");
    expect(serviceWorkerURL(PROXY_BASE.replace(/\/$/, ""))).toBe(PROXY_BASE + "ui/capture-sw.js");
  });
});

describe("isEnabled", () => {
  it("requires an explicit grant", () => {
    expect(isEnabled({ getItem: () => "granted" })).toBe(true);
    expect(isEnabled({ getItem: () => null })).toBe(false);
    expect(isEnabled({ getItem: () => "denied" })).toBe(false);
  });

  it("treats unavailable storage as no consent", () => {
    expect(
      isEnabled({
        getItem: () => {
          throw new Error("storage disabled");
        },
      }),
    ).toBe(false);
  });
});

describe("registerAll", () => {
  it("claims both Talk call scopes and never the root", async () => {
    const { container, registered } = fakeContainer();
    const outcomes = await registerAll(container, PROXY_BASE, "");
    expect(outcomes.every((o) => o.ok)).toBe(true);
    expect(registered.map((r) => r.scope)).toEqual(["/call/", "/index.php/call/"]);
    expect(registered.map((r) => r.scope)).not.toContain("/");
  });

  it("reports a failing scope without abandoning the other", async () => {
    const container = {
      register: vi.fn(async (_url: string, options?: { scope?: string }) => {
        if (options?.scope === "/call/") throw new Error("SecurityError");
        return {} as ServiceWorkerRegistration;
      }),
    } as unknown as ServiceWorkerContainer;
    const outcomes = await registerAll(container, PROXY_BASE, "");
    expect(outcomes[0].ok).toBe(false);
    expect(outcomes[1].ok).toBe(true);
  });
});

describe("unregisterAll", () => {
  it("removes only our own scopes, leaving core's root worker alone", async () => {
    const { container, unregistered } = fakeContainer();
    await unregisterAll(container, "");
    expect(unregistered).toEqual(["/call/"]);
  });
});

describe("setUp", () => {
  it("does nothing without consent", async () => {
    const { container, registered } = fakeContainer();
    await setUp(container, { getItem: () => null }, PROXY_BASE, "");
    expect(registered).toHaveLength(0);
  });

  it("registers once consent is recorded", async () => {
    const { container, registered } = fakeContainer();
    await setUp(container, { getItem: () => "granted" }, PROXY_BASE, "");
    expect(registered).toHaveLength(2);
  });

  it("is a no-op in a browser without service workers", async () => {
    await expect(setUp(undefined, { getItem: () => "granted" }, PROXY_BASE, "")).resolves.toEqual([]);
  });
});
