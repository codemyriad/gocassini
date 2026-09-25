import { afterEach, describe, expect, it, vi } from "vitest";
import { OperatorClient } from "./client";

afterEach(() => vi.unstubAllGlobals());
function reply(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
}

describe("recordings storage API", () => {
  it("reads a single setup plan and treats missing browser permission as false", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => reply({
      first_run: true, service_account: { user: "cassini", known: true, exists: false },
      ok: false, state: "unavailable", step: "owner_account",
      setup: [{ id: "owner", action: "create_user", args: { user: "cassini" } }],
    })));
    const status = await new OperatorClient("/operator").getStorage();
    expect(status.setup).toEqual([{
      id: "owner", action: "create_user", title: "", args: { user: "cassini" },
      browser: false, occ: "",
    }]);
    expect(status.service_account.exists).toBe(false);
  });
  it("rechecks and acknowledges using the two supported actions", async () => {
    const fetchMock = vi.fn(async () => reply({ ok: true, state: "provisioned", setup: [] }));
    vi.stubGlobal("fetch", fetchMock);
    const client = new OperatorClient("/operator");
    await client.recheckStorage();
    await client.acknowledgeFirstRun();
    expect(JSON.parse(String(fetchMock.mock.calls[0][1].body))).toEqual({ action: "recheck" });
    expect(JSON.parse(String(fetchMock.mock.calls[1][1].body))).toEqual({ action: "acknowledge_first_run" });
  });
});
