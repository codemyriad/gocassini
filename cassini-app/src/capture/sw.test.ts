import { describe, expect, it, vi } from "vitest";
import { composeBundle, handleFetch, shouldRewrite } from "./sw";

const TALK_URL = "https://cloud.example.com/apps/spreed/js/talk-main.mjs";
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
  const talkRequest = () => new Request(TALK_URL);

  it("accepts a plain 200 JavaScript response carrying Talk's own code", () => {
    expect(shouldRewrite(talkRequest(), talkResponse(), "OCA.Talk")).toBe(true);
  });

  it("refuses a range request, whose body is one fragment of the script", () => {
    const request = new Request(TALK_URL, { headers: { range: "bytes=0-100" } });
    expect(shouldRewrite(request, talkResponse(), "OCA.Talk")).toBe(false);
  });

  it("refuses a partial-content response", () => {
    expect(shouldRewrite(talkRequest(), talkResponse("OCA", { status: 206 }), "OCA")).toBe(false);
  });

  it("refuses a response that is not JavaScript", () => {
    const html = new Response("<html>login</html>", {
      status: 200,
      headers: { "content-type": "text/html" },
    });
    expect(shouldRewrite(talkRequest(), html, "<html>login</html>")).toBe(false);
  });

  it("refuses a JavaScript response that is not Talk's bundle", () => {
    // A proxy notice or an error page served at the bundle's URL.
    expect(shouldRewrite(talkRequest(), talkResponse(), "throw new Error('gateway')")).toBe(false);
  });
});

describe("handleFetch", () => {
  it("declines anything that is not Talk's bundle", async () => {
    const fetchImpl = vi.fn();
    const result = await handleFetch(
      new Request("https://cloud.example.com/apps/files/js/main.mjs"),
      fetchImpl as never,
      PAYLOAD,
    );
    expect(result).toBeNull();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("appends the payload to Talk's bundle", async () => {
    const fetchImpl = vi.fn(async () =>
      talkResponse("OCA.Talk = {}; TALK", { headers: { "content-length": "19", etag: "\"abc\"" } }),
    );
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, PAYLOAD);
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
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, "");
    expect(await result!.text()).toBe("OCA.Talk = {}; TALK");
  });

  it("serves the response untouched when it is not really Talk's bundle", async () => {
    const fetchImpl = vi.fn(
      async () => new Response("<html>login</html>", { status: 200, headers: { "content-type": "text/html" } }),
    );
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, PAYLOAD);
    expect(await result!.text()).toBe("<html>login</html>");
  });

  it("passes an upstream failure through rather than inventing a bundle", async () => {
    const fetchImpl = vi.fn(async () => new Response("boom", { status: 500 }));
    const result = await handleFetch(new Request(TALK_URL), fetchImpl as never, PAYLOAD);
    expect(result!.status).toBe(500);
  });
});
