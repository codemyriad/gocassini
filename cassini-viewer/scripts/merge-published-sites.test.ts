import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";

import {
  mergePublishedSites,
  normalizeSiteAssetPath,
  resolveSiteAssetPath,
} from "./merge-published-sites.mjs";

const tempDirs: string[] = [];

function makeTempDir(prefix: string): string {
  const dir = mkdtempSync(join(tmpdir(), prefix));
  tempDirs.push(dir);
  return dir;
}

function writeJson(path: string, value: unknown) {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

afterEach(() => {
  while (tempDirs.length > 0) {
    const dir = tempDirs.pop();
    if (dir) {
      rmSync(dir, { recursive: true, force: true });
    }
  }
});

describe("mergePublishedSites", () => {
  it("merges catalogs and referenced meeting artifacts", () => {
    const primaryDir = makeTempDir("cassini-merge-primary-");
    const secondaryDir = makeTempDir("cassini-merge-secondary-");
    const outputDir = makeTempDir("cassini-merge-output-");

    writeFileSync(join(primaryDir, "index.html"), "<html>primary</html>\n", "utf8");
    mkdirSync(join(primaryDir, "assets"), { recursive: true });
    writeFileSync(join(primaryDir, "assets", "primary.js"), "console.log('primary');\n", "utf8");
    writeJson(join(primaryDir, "cassini.json"), {
      kind: "site",
      version: "cassini.site.v1",
      source_path: "/tmp/primary",
      catalog_path: "catalog.json",
      meeting_count: 2,
    });
    writeJson(join(primaryDir, "catalog.json"), {
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "meeting-a",
          artifactPath: "./meetings/meeting-a",
          title: "Meeting A",
          dateLabel: "2026-05-01 09:00",
        },
        {
          id: "meeting-b",
          artifactPath: "./meetings/meeting-b",
          title: "Meeting B (primary)",
          dateLabel: "2026-05-02 09:00",
        },
      ],
    });
    writeJson(join(primaryDir, "meetings", "meeting-a", "manifest.json"), { source: "primary-a" });
    writeJson(join(primaryDir, "meetings", "meeting-b", "manifest.json"), { source: "primary-b" });
    writeFileSync(join(primaryDir, "meetings", "meeting-b", "stale.txt"), "stale\n", "utf8");

    mkdirSync(join(secondaryDir, "assets"), { recursive: true });
    writeFileSync(join(secondaryDir, "assets", "secondary.js"), "console.log('secondary');\n", "utf8");
    writeJson(join(secondaryDir, "catalog.json"), {
      version: "cassini.viewer.catalog.v1",
      meetings: [
        {
          id: "meeting-b",
          artifactPath: "./meetings/meeting-b",
          title: "Meeting B (secondary)",
          dateLabel: "2026-05-03 09:00",
        },
        {
          id: "meeting-c",
          audioPath: "./meetings/meeting-c.opus",
          title: "Meeting C",
          dateLabel: "2026-05-04 09:00",
        },
      ],
    });
    writeJson(join(secondaryDir, "meetings", "meeting-b", "manifest.json"), { source: "secondary-b" });
    mkdirSync(join(secondaryDir, "meetings"), { recursive: true });
    writeFileSync(join(secondaryDir, "meetings", "meeting-c.opus"), "portable-audio", "utf8");

    const merged = mergePublishedSites({
      primaryDir,
      secondaryDir,
      outputDir,
      prefer: "second",
    });

    expect(merged.meetings.map((meeting) => meeting.id)).toEqual(["meeting-a", "meeting-b", "meeting-c"]);
    expect(merged.meetings[1]?.title).toBe("Meeting B (secondary)");

    expect(readFileSync(join(outputDir, "index.html"), "utf8")).toContain("primary");
    expect(existsSync(join(outputDir, "assets", "primary.js"))).toBe(true);
    expect(existsSync(join(outputDir, "assets", "secondary.js"))).toBe(true);
    expect(JSON.parse(readFileSync(join(outputDir, "meetings", "meeting-b", "manifest.json"), "utf8"))).toEqual({
      source: "secondary-b",
    });
    expect(existsSync(join(outputDir, "meetings", "meeting-b", "stale.txt"))).toBe(false);
    expect(readFileSync(join(outputDir, "meetings", "meeting-c.opus"), "utf8")).toBe("portable-audio");

    const mergedManifest = JSON.parse(readFileSync(join(outputDir, "cassini.json"), "utf8"));
    expect(mergedManifest.meeting_count).toBe(3);
    expect(mergedManifest.catalog_path).toBe("catalog.json");
    expect(mergedManifest.source_path).toContain(primaryDir);
    expect(mergedManifest.source_path).toContain(secondaryDir);
  });

  // D-531: lightweight published sites have no viewer shell. Merging two of them
  // must produce a shell-less merged site (catalog + meetings only).
  it("merges lightweight sites without a viewer shell", () => {
    const primaryDir = makeTempDir("merge-light-primary-");
    const secondaryDir = makeTempDir("merge-light-secondary-");
    const outputDir = makeTempDir("merge-light-out-");

    for (const [dir, id] of [
      [primaryDir, "alpha"],
      [secondaryDir, "beta"],
    ] as const) {
      writeJson(join(dir, "catalog.json"), {
        version: "cassini.viewer.catalog.v1",
        meetings: [{ id, title: id, dateLabel: "2026-03-04", audioPath: `./meetings/${id}.opus` }],
      });
      mkdirSync(join(dir, "meetings"), { recursive: true });
      writeFileSync(join(dir, "meetings", `${id}.opus`), `opus-${id}`, "utf8");
    }

    const merged = mergePublishedSites({ primaryDir, secondaryDir, outputDir });

    expect(merged.meetings.map((m) => m.id).sort()).toEqual(["alpha", "beta"]);
    expect(existsSync(join(outputDir, "catalog.json"))).toBe(true);
    expect(existsSync(join(outputDir, "meetings", "alpha.opus"))).toBe(true);
    expect(existsSync(join(outputDir, "meetings", "beta.opus"))).toBe(true);
    // No viewer shell was present in the inputs, so none appears in the output.
    expect(existsSync(join(outputDir, "index.html"))).toBe(false);
    expect(existsSync(join(outputDir, "assets"))).toBe(false);
  });

  it("rejects asset paths that escape the site root", () => {
    const siteDir = makeTempDir("cassini-merge-site-");
    expect(normalizeSiteAssetPath("./meetings/demo")).toBe("meetings/demo");
    expect(() => resolveSiteAssetPath(siteDir, "../outside.json")).toThrow(/escapes site root/i);
  });
});
