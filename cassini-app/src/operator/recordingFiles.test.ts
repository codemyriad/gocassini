import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { findRecordingInFiles, pathFromMultistatus, recordingFilesUrl, searchBody } from "./recordingFiles";

function multistatus(...hrefs: string[]): string {
  const responses = hrefs
    .map((href) => `<d:response><d:href>${href}</d:href><d:propstat><d:prop><oc:fileid>7301</oc:fileid></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
    .join("");
  return `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">${responses}</d:multistatus>`;
}

function stubFetch(status: number, body: string) {
  return vi.fn(async (_url: RequestInfo | URL, _init?: RequestInit) => new Response(body, { status }));
}

beforeEach(() => {
  vi.stubGlobal("OC", {
    requestToken: "token",
    getRootPath: () => "/nextcloud",
    getCurrentUser: () => ({ uid: "ada lovelace" }),
  });
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("searchBody", () => {
  it("searches the user's whole Files tree for the one file id", () => {
    const body = searchBody("a<b", 7301);
    expect(body).toContain("<d:href>/files/a&#60;b</d:href>");
    expect(body).toContain("<d:depth>infinity</d:depth>");
    expect(body).toContain("<d:literal>7301</d:literal>");
  });
});

describe("pathFromMultistatus", () => {
  it("is the path in the user's own Files, decoded", () => {
    const xml = multistatus("/nextcloud/remote.php/dav/files/ada%20lovelace/Shared/J%201.opus");
    expect(pathFromMultistatus(xml, "ada lovelace")).toBe("Shared/J 1.opus");
  });

  it("is empty when Nextcloud found nothing, or named another tree", () => {
    expect(pathFromMultistatus(multistatus(), "ada")).toBe("");
    expect(pathFromMultistatus(multistatus("/remote.php/dav/files/cassini/J.opus"), "ada")).toBe("");
  });
});

describe("findRecordingInFiles", () => {
  it("asks Nextcloud as the signed-in user and returns where the file is for them", async () => {
    const fetchImpl = stubFetch(207, multistatus("/nextcloud/remote.php/dav/files/ada%20lovelace/J.opus"));
    await expect(findRecordingInFiles(7301, fetchImpl)).resolves.toBe("J.opus");
    const [url, init] = fetchImpl.mock.calls[0];
    expect(url).toBe("/nextcloud/remote.php/dav/");
    expect(init?.method).toBe("SEARCH");
    expect(init?.credentials).toBe("same-origin");
    expect((init?.headers as Record<string, string>).requesttoken).toBe("token");
  });

  it("finds nothing for a recording the user cannot open", async () => {
    await expect(findRecordingInFiles(7301, stubFetch(207, multistatus()))).resolves.toBe("");
  });

  it("logs a failed lookup and finds nothing", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await expect(findRecordingInFiles(7301, stubFetch(500, ""))).resolves.toBe("");
    const failing = vi.fn(async () => {
      throw new Error("offline");
    });
    await expect(findRecordingInFiles(7301, failing)).resolves.toBe("");
    expect(warn).toHaveBeenCalledTimes(2);
  });

  it("does not ask without a file id or a signed-in user", async () => {
    const fetchImpl = stubFetch(207, multistatus());
    await expect(findRecordingInFiles(0, fetchImpl)).resolves.toBe("");
    vi.stubGlobal("OC", { getRootPath: () => "" });
    await expect(findRecordingInFiles(7301, fetchImpl)).resolves.toBe("");
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

describe("recordingFilesUrl", () => {
  it("opens the file with its details sidebar", () => {
    const page = { origin: "https://cloud.example", pathname: "/nextcloud/index.php/apps/app_api/embedded/gocassini" };
    expect(recordingFilesUrl(page, "Shared/J.opus", 7301)).toBe(
      "https://cloud.example/nextcloud/apps/files/files/7301?dir=%2FShared&opendetails",
    );
  });
});
