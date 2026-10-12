import { describe, expect, it } from "vitest";

import { insightFilesUrl, nextcloudRoot } from "./filesUrl";

const PATH = "Cassini Insights/2026-10-12-summarise-ins_0123456789abcdef.md";
const QUERY = "?dir=%2FCassini%20Insights&scrollto=2026-10-12-summarise-ins_0123456789abcdef.md";

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
    expect(insightFilesUrl(page, PATH, "/apps/cloud")).toBe(`https://host.example/apps/cloud/apps/files/${QUERY}`);
  });

  it("keeps a subpath install's prefix", () => {
    expect(nextcloudRoot(at("https://example.org/nextcloud/apps/app_api/embedded/gocassini/viewer"))).toBe(
      "https://example.org/nextcloud",
    );
    expect(nextcloudRoot(at("https://example.org/nextcloud/index.php/apps/app_api/embedded/gocassini"))).toBe(
      "https://example.org/nextcloud",
    );
  });
});

describe("insightFilesUrl", () => {
  it("opens the folder scrolled to the document from a bare origin", () => {
    expect(insightFilesUrl(at("https://cloud.example"), PATH)).toBe(`https://cloud.example/apps/files/${QUERY}`);
  });

  it("opens Files from the AppAPI proxy page", () => {
    expect(
      insightFilesUrl(at("https://cloud.example/index.php/apps/app_api/proxy/gocassini/viewer#meeting=m1"), PATH),
    ).toBe(`https://cloud.example/apps/files/${QUERY}`);
  });

  it("keeps a subpath install's prefix", () => {
    expect(insightFilesUrl(at("https://example.org/nextcloud/apps/app_api/embedded/gocassini/viewer"), PATH)).toBe(
      `https://example.org/nextcloud/apps/files/${QUERY}`,
    );
  });

  it("opens the home folder for a document at the top level", () => {
    expect(insightFilesUrl(at("https://cloud.example/"), "answer.md")).toBe(
      "https://cloud.example/apps/files/?dir=%2F&scrollto=answer.md",
    );
  });

  it("is empty when the run has no document", () => {
    expect(insightFilesUrl(at("https://cloud.example/"), "")).toBe("");
  });
});
