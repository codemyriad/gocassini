import { describe, expect, it } from "vitest";

import { insightFilesUrl, nextcloudRoot } from "./filesUrl";

const PATH = "Cassini Insights/2026-10-12-summarise-ins_0123456789abcdef.md";
// Without a file id the folder is all the Files app can be pointed at.
const QUERY = "?dir=%2FCassini%20Insights";

function at(href: string) {
  const url = new URL(href);
  return { origin: url.origin, pathname: url.pathname };
}

describe("nextcloudRoot", () => {
  it("is the origin when the page names no Nextcloud route", () => {
    expect(nextcloudRoot(at("https://cloud.example/"))).toBe("https://cloud.example");
  });

  it("cuts at /index.php/ on the AppAPI proxy page", () => {
    expect(nextcloudRoot(at("https://cloud.example/index.php/apps/app_api/proxy/gocassini/viewer"))).toBe(
      "https://cloud.example",
    );
  });

  it("takes Nextcloud's own word for the root over the path when it is on the page", () => {
    // A subpath that itself contains /apps/ is where the guess goes wrong.
    const page = at("https://host.example/apps/cloud/index.php/apps/app_api/embedded/gocassini/viewer");
    expect(nextcloudRoot(page, "/apps/cloud")).toBe("https://host.example/apps/cloud");
    expect(nextcloudRoot(page, "/apps/cloud/")).toBe("https://host.example/apps/cloud");
    // "" is an answer — the root is "/" — and only null means nobody said.
    expect(nextcloudRoot(page, "")).toBe("https://host.example");
    expect(nextcloudRoot(page, null)).toBe("https://host.example");
    expect(insightFilesUrl(page, PATH, "/apps/cloud")).toBe(`https://host.example/apps/cloud/apps/files/files${QUERY}`);
  });

  it("keeps a subpath install's prefix", () => {
    expect(nextcloudRoot(at("https://example.org/nextcloud/apps/app_api/embedded/gocassini/viewer"))).toBe(
      "https://example.org/nextcloud",
    );
    expect(nextcloudRoot(at("https://example.org/nextcloud/index.php/apps/app_api/embedded/gocassini"))).toBe(
      "https://example.org/nextcloud",
    );
  });

  it("cuts at /exapps/ on a page HaRP serves for the ExApp itself", () => {
    expect(nextcloudRoot(at("https://example.org/nextcloud/exapps/gocassini/viewer"))).toBe(
      "https://example.org/nextcloud",
    );
  });
});

describe("insightFilesUrl", () => {
  it("selects the document by its file id and opens its details", () => {
    // The sidebar is where Nextcloud's own sharing is, which is what this
    // link is for.
    expect(insightFilesUrl(at("https://cloud.example"), PATH, null, "4242")).toBe(
      `https://cloud.example/apps/files/files/4242${QUERY}&opendetails`,
    );
  });

  it("opens the folder when the run has no file id", () => {
    expect(insightFilesUrl(at("https://cloud.example"), PATH)).toBe(`https://cloud.example/apps/files/files${QUERY}`);
    expect(insightFilesUrl(at("https://cloud.example"), PATH, null, "")).toBe(
      `https://cloud.example/apps/files/files${QUERY}`,
    );
    // Only digits are a file id; anything else must not become a path segment.
    expect(insightFilesUrl(at("https://cloud.example"), PATH, null, "42/../x")).toBe(
      `https://cloud.example/apps/files/files${QUERY}`,
    );
  });

  it("opens Files from the AppAPI proxy page", () => {
    expect(
      insightFilesUrl(at("https://cloud.example/index.php/apps/app_api/proxy/gocassini/viewer#meeting=m1"), PATH, null, "7"),
    ).toBe(`https://cloud.example/apps/files/files/7${QUERY}&opendetails`);
  });

  it("keeps a subpath install's prefix", () => {
    expect(insightFilesUrl(at("https://example.org/nextcloud/apps/app_api/embedded/gocassini/viewer"), PATH)).toBe(
      `https://example.org/nextcloud/apps/files/files${QUERY}`,
    );
  });

  it("encodes a folder for a document named after its insight", () => {
    // Files are named after the insight now (D-791): spaces, a middle dot, a
    // hash. Only the folder is in the URL, and it is encoded.
    expect(insightFilesUrl(at("https://cloud.example"), "Cassini Insights/Design sync · 3 meetings #2.md", null, "9")).toBe(
      "https://cloud.example/apps/files/files/9?dir=%2FCassini%20Insights&opendetails",
    );
    expect(insightFilesUrl(at("https://cloud.example"), "A & B/What did we decide.md")).toBe(
      "https://cloud.example/apps/files/files?dir=%2FA%20%26%20B",
    );
  });

  it("opens the home folder for a document at the top level", () => {
    expect(insightFilesUrl(at("https://cloud.example/"), "answer.md")).toBe(
      "https://cloud.example/apps/files/files?dir=%2F",
    );
  });

  it("is empty when the run has no document", () => {
    expect(insightFilesUrl(at("https://cloud.example/"), "")).toBe("");
  });
});
