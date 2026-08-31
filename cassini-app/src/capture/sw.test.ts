import { describe, expect, it, vi } from "vitest";
import { composeBundle, handleFetch, payloadURL } from "./sw";

const SW_URL = "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/ui/capture-sw.js";
const TALK_URL = "https://cloud.example.com/apps/spreed/js/talk-main.mjs";

describe("payloadURL", () => {
  it("resolves the payload next to the service worker script", () => {
    expect(payloadURL(SW_URL)).toBe(
      "https://cloud.example.com/index.php/apps/app_api/proxy/gocassini/ui/capture-payload.js",
    );
  });
});

describe("composeBundle", () => {
  it("terminates Talk's last statement before the payload starts", () => {
    const composed = composeBundle("const a = 1", "console.log(1)");
    expect(composed).toContain("const a = 1\n;\n");
    expect(composed).toContain("console.log(1)");
  });
});

describe("handleFetch", () => {
  it("declines anything that is not Talk's bundle", async () => {
    const fetchImpl = vi.fn();
    const result = await handleFetch(new Request("https://cloud.example.com/apps/files/js/main.mjs"), fetchImpl as never, SW_URL);
    expect(result).toBeNull();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("appends the payload to Talk's bundle", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof Request ? input.url : input.toString();
      if (url.includes("capture-payload.js")) {
        return new Response("PAYLOAD", { status: 200 });
      }
      return new Response("TALK", { status: 200, headers: { "content-type": "text/javascript", "content-length": "4" } });
    });
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, SW_URL);
    const body = await result!.text();
    expect(body).toContain("TALK");
    expect(body).toContain("PAYLOAD");
    expect(result!.headers.get("content-type")).toBe("text/javascript");
    // A stale Content-Length would truncate the script we just made longer.
    expect(result!.headers.get("content-length")).toBeNull();
  });

  it("serves Talk untouched when the payload cannot be fetched", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof Request ? input.url : input.toString();
      if (url.includes("capture-payload.js")) {
        throw new Error("ExApp down");
      }
      return new Response("TALK", { status: 200 });
    });
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, SW_URL);
    expect(await result!.text()).toBe("TALK");
  });

  it("serves Talk untouched when the payload responds with an error", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input instanceof Request ? input.url : input.toString();
      if (url.includes("capture-payload.js")) {
        return new Response("nope", { status: 503 });
      }
      return new Response("TALK", { status: 200 });
    });
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, SW_URL);
    expect(await result!.text()).toBe("TALK");
  });

  it("passes an upstream failure through rather than inventing a bundle", async () => {
    const fetchImpl = vi.fn(async () => new Response("boom", { status: 500 }));
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, SW_URL);
    expect(result!.status).toBe(500);
  });
});
