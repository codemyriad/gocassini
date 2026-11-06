import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { browserSupportContext, recordCatalogDiagnostic, requestSupportBundle } from "./client";

vi.mock("cassini-viewer/dataProvider", () => ({ resolvePublishedUrl: (name: string) => `https://cloud.test/index.php/apps/app_api/proxy/gocassini/published/${name}` }));
vi.mock("../operator/config", () => ({ loadConfig: () => ({ operatorBasePath: "https://cloud.test/index.php/apps/app_api/proxy/gocassini/operator" }) }));

beforeEach(() => {
  vi.stubGlobal("navigator", { onLine: true });
  recordCatalogDiagnostic("not_requested");
});
afterEach(() => vi.unstubAllGlobals());
const zip = () => new Response(new Blob(["PK-test-zip"]), { headers: { "Content-Type": "application/zip", "Content-Disposition": 'attachment; filename="cassini-support-20261102T120000Z-123456abcdef.zip"' } });

describe("support download", () => {
  it("uses the USER proxy path with small context and the caller's session", async () => {
    const fetchMock = vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => zip());
    vi.stubGlobal("fetch", fetchMock);
    recordCatalogDiagnostic("failed");
    const signal = new AbortController().signal;
    const result = await requestSupportBundle(false, "browse", signal);
    expect(fetchMock).toHaveBeenCalledWith("https://cloud.test/index.php/apps/app_api/proxy/gocassini/support/bundle", expect.objectContaining({ method: "POST", credentials: "same-origin", signal }));
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({ privacy:"original",surface: "browse", timezone_offset_minutes: expect.any(Number), online: true, catalog_state: "failed", catalog_reason: "request_failed" });
    expect(result.filename).toBe("cassini-support-20261102T120000Z-123456abcdef.zip");
    expect(result.administrator).toBe(false);
  });

  it("uses the separate ADMIN path and falls back only when permissions were revoked", async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(null, { status: 403 })).mockResolvedValueOnce(zip());
    vi.stubGlobal("fetch", fetchMock);
    const result = await requestSupportBundle(true, "operator", new AbortController().signal);
    expect(fetchMock.mock.calls.map(call => call[0])).toEqual([
      "https://cloud.test/index.php/apps/app_api/proxy/gocassini/operator/support-bundle",
      "https://cloud.test/index.php/apps/app_api/proxy/gocassini/support/bundle",
    ]);
    expect(result.administrator).toBe(false);
  });

  it.each([401, 404, 429, 500])("reports HTTP %s without downloading the error body", async status => {
    const fetchMock = vi.fn(async () => new Response("PRIVATE ERROR TEXT", { status }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(requestSupportBundle(true, "operator", new AbortController().signal)).rejects.toThrow(/sign in|available|prepared|prepare/i);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("rejects a login page returned with HTTP 200", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("<html>Sign in</html>", { headers: { "Content-Type": "text/html" } })));
    await expect(requestSupportBundle(false, "browse", new AbortController().signal)).rejects.toThrow("didn’t return a support bundle");
  });

  it("includes original catalog failure text only when filtering is off", () => {
    recordCatalogDiagnostic("failed", true,"Unfamiliar catalog failure: private URL");
    expect(browserSupportContext("browse")).toMatchObject({ catalog_state: "failed", catalog_reason: "network_error" });
    expect(browserSupportContext("browse","scrubbed")).not.toHaveProperty("catalog_error");
    expect(browserSupportContext("browse","original")).toHaveProperty("catalog_error","Unfamiliar catalog failure: private URL");
    recordCatalogDiagnostic("loaded");
    expect(browserSupportContext("browse")).toMatchObject({ catalog_state: "loaded", catalog_reason: "none" });
    expect(browserSupportContext("browse","original")).not.toHaveProperty("catalog_error");
  });

  it("keeps the requested scrubbing mode when falling back to a user bundle", async () => {
    const fetchMock=vi.fn().mockResolvedValueOnce(new Response(null,{status:403})).mockResolvedValueOnce(zip());
    vi.stubGlobal("fetch",fetchMock);
    await requestSupportBundle(true,"browse",new AbortController().signal,"scrubbed");
    for (const call of fetchMock.mock.calls) expect(JSON.parse(call[1].body).privacy).toBe("scrubbed");
  });

  it.each(["original", "scrubbed"] as const)("keeps %s in the downloaded filename",async privacy=>{
    const filename=`cassini-support-${privacy}-20261102T120000Z-123456abcdef.zip`;
    vi.stubGlobal("fetch",vi.fn(async()=>new Response(new Blob(["PK-test"]),{headers:{"Content-Type":"application/zip","Content-Disposition":`attachment; filename="${filename}"`}})));
    const result=await requestSupportBundle(false,"browse",new AbortController().signal,privacy);
    expect(result.filename).toBe(filename);
  });
});
