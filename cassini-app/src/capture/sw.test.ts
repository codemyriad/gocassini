import { describe, expect, it, vi } from "vitest";
import { composeBundle, handleFetch, shouldRewrite } from "./sw";

const ORIGIN = "https://cloud.example.com";
const TALK_URL = `${ORIGIN}/apps/spreed/js/talk-main.mjs`;
const PAYLOAD = "/* payload */ console.log(1)";

// talkResponse mimics a real bundle response: a JavaScript content type and a
// body carrying the sentinel every Talk bundle has.
function talkResponse(body = "OCA.Talk = {}; TALK", init: ResponseInit = {}) {
  // Spread init FIRST: putting it last would replace the merged headers with
  // the caller's alone and quietly drop the content type.
  return new Response(body, {
    status: 200,
    ...init,
    headers: { "content-type": "text/javascript", ...(init.headers ?? {}) },
  });
}

describe("composeBundle", () => {
  it("terminates Talk's last statement before the payload starts", () => {
    const composed = composeBundle("const a = 1", "console.log(1)");
    expect(composed).toContain("const a = 1\n;\n");
    expect(composed).toContain("console.log(1)");
  });
});

// Every one of these guards exists because getting it wrong corrupts another
// application's script rather than merely disabling our own feature.
describe("shouldRewrite", () => {
  // Requests the browser makes for a <script> carry destination "script";
  // undici's Request defaults to "", so the tests state it explicitly.
  const scriptRequest = (url = TALK_URL, init: RequestInit = {}) =>
    Object.defineProperty(new Request(url, init), "destination", { value: "script" });

  it("accepts a same-origin 200 script response carrying Talk's own code", () => {
    expect(shouldRewrite(scriptRequest(), talkResponse(), "OCA.Talk", ORIGIN)).toBe(true);
  });

  it("refuses a range request, whose body is one fragment of the script", () => {
    const request = scriptRequest(TALK_URL, { headers: { range: "bytes=0-100" } });
    expect(shouldRewrite(request, talkResponse(), "OCA.Talk", ORIGIN)).toBe(false);
  });

  it("refuses a partial-content response", () => {
    expect(shouldRewrite(scriptRequest(), talkResponse("OCA", { status: 206 }), "OCA", ORIGIN)).toBe(false);
  });

  it("refuses a response that is not JavaScript", () => {
    const html = new Response("<html>login</html>", {
      status: 200,
      headers: { "content-type": "text/html" },
    });
    expect(shouldRewrite(scriptRequest(), html, "<html>login</html>", ORIGIN)).toBe(false);
  });

  it("refuses a JavaScript response that is not Talk's bundle", () => {
    // A proxy notice or an error page served at the bundle's URL.
    expect(shouldRewrite(scriptRequest(), talkResponse(), "throw new Error('gateway')", ORIGIN)).toBe(false);
  });

  it("refuses anything that is not being loaded as a script", () => {
    // An XHR or a prefetch for the same URL is not Talk evaluating its bundle,
    // and its caller did not ask for our payload.
    const xhr = new Request(TALK_URL); // destination ""
    expect(shouldRewrite(xhr, talkResponse(), "OCA.Talk", ORIGIN)).toBe(false);
  });

  it("refuses a cross-origin script even when the path matches", () => {
    const foreign = scriptRequest("https://evil.example.net/apps/spreed/js/talk-main.mjs");
    expect(shouldRewrite(foreign, talkResponse(), "OCA.Talk", ORIGIN)).toBe(false);
  });
});

describe("handleFetch", () => {
  it("declines anything that is not Talk's bundle", async () => {
    const fetchImpl = vi.fn();
    const result = await handleFetch(
      new Request("https://cloud.example.com/apps/files/js/main.mjs"),
      fetchImpl as never,
      PAYLOAD,
      ORIGIN,
    );
    expect(result).toBeNull();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("appends the payload to Talk's bundle", async () => {
    const scriptRequest = Object.defineProperty(new Request(TALK_URL), "destination", { value: "script" });
    const fetchImpl = vi.fn(async () =>
      talkResponse("OCA.Talk = {}; TALK", { headers: { "content-length": "19", etag: "\"abc\"" } }),
    );
    const result = await handleFetch(scriptRequest, fetchImpl as never, PAYLOAD, ORIGIN);
    const body = await result!.text();

    expect(body).toContain("TALK");
    expect(body).toContain("/* payload */");
    expect(result!.headers.get("content-type")).toBe("text/javascript");
    // A stale Content-Length truncates the script we just made longer, and a
    // stale validator invites a cache to revalidate modified bytes against the
    // original's identity.
    expect(result!.headers.get("content-length")).toBeNull();
    expect(result!.headers.get("etag")).toBeNull();
  });

  it("serves Talk untouched when there is no payload to append", async () => {
    const fetchImpl = vi.fn(async () => talkResponse());
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, "", ORIGIN);
    expect(await result!.text()).toBe("OCA.Talk = {}; TALK");
  });

  it("serves the response untouched when it is not really Talk's bundle", async () => {
    const fetchImpl = vi.fn(
      async () => new Response("<html>login</html>", { status: 200, headers: { "content-type": "text/html" } }),
    );
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, PAYLOAD, ORIGIN);
    expect(await result!.text()).toBe("<html>login</html>");
  });

  it("passes an upstream failure through rather than inventing a bundle", async () => {
    const fetchImpl = vi.fn(async () => new Response("boom", { status: 500 }));
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, PAYLOAD, ORIGIN);
    expect(result!.status).toBe(500);
  });
});
