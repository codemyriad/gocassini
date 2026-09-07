import { execFileSync, spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

import {
  canonicalPortableMeetingName,
  isReadyMeetingBundle,
  listArtifactDirectories,
  packArtifactDirectory,
} from "./pack-portable-artifacts.mjs";

// Mirrors writeReadyMeetingBundleFixture in cassini-go-recorder's
// internal/cassini/cli_test.go: the minimal directory `cassini pack` accepts.
// With realAudio the audio is an actual Opus-in-WebM (needs ffmpeg); without
// it, a placeholder good enough for discovery tests, which only stat files.
function writeReadyBundleFixture(bundleDir: string, opts: { realAudio?: boolean; state?: string } = {}) {
  mkdirSync(bundleDir, { recursive: true });
  if (opts.realAudio) {
    execFileSync("ffmpeg", [
      "-y",
      "-v",
      "error",
      "-f",
      "lavfi",
      "-i",
      "sine=frequency=660:sample_rate=48000:duration=0.25",
      "-c:a",
      "libopus",
      "-application",
      "voip",
      join(bundleDir, "meeting.webm"),
    ]);
  } else {
    writeFileSync(join(bundleDir, "meeting.webm"), "placeholder-audio");
  }
  writeFileSync(
    join(bundleDir, "transcript.words.v1.json"),
    JSON.stringify({
      version: "transcript.words.v1",
      media: { src: "meeting.webm", durationMs: 250 },
      speakers: [{ id: "spk_host", label: "Host" }],
      segments: [
        {
          speaker: "spk_host",
          startMs: 0,
          endMs: 200,
          text: "hello team",
          words: [
            { text: "hello", startMs: 0, endMs: 80 },
            { text: "team", startMs: 100, endMs: 200 },
          ],
        },
      ],
    }),
  );
  writeFileSync(
    join(bundleDir, "manifest.json"),
    JSON.stringify({
      version: "cassini.meeting-artifact.v1",
      generatedAt: "2026-03-11T10:00:00Z",
      source: { basename: "source.mkv", durationMs: 250 },
      files: { audio: "meeting.webm", transcript: "transcript.words.v1.json" },
      speakerCount: 1,
      wordCount: 2,
    }),
  );
  writeFileSync(
    join(bundleDir, "cassini.json"),
    JSON.stringify({
      kind: "meeting",
      version: "cassini.meeting.v1",
      created_at_utc: "2026-03-11T10:00:00Z",
      state: opts.state ?? "ready",
      stage: opts.state ?? "ready",
      source_kind: "mkv",
      source_path: "/tmp/source.mkv",
      files: {
        audio: "meeting.webm",
        transcript: "transcript.words.v1.json",
        artifact_manifest: "manifest.json",
      },
    }),
  );
}

function toolAvailable(command: string, args: string[]): boolean {
  try {
    return spawnSync(command, args, { stdio: "ignore" }).status === 0;
  } catch {
    return false;
  }
}

const hasRealCliToolchain = toolAvailable("go", ["version"]) && toolAvailable("ffmpeg", ["-version"]);
if (!hasRealCliToolchain) {
  console.warn(
    "pack-portable-artifacts: skipping the real-CLI pack test — it needs `go` and `ffmpeg` on PATH",
  );
}

describe("packArtifactDirectory", () => {
  it("delegates published portable production to the canonical Go packer", async () => {
    const calls: unknown[][] = [];
    const result = await packArtifactDirectory(
      "/tmp/example.meeting",
      "/tmp/example.opus",
      (...args: unknown[]) => calls.push(args),
    );

    expect(result).toEqual({ status: "write" });
    expect(calls).toEqual([
      [
        "cassini",
        ["pack", "/tmp/example.meeting", "--out", "/tmp/example.opus"],
        { stdio: "inherit" },
      ],
    ]);
  });
});

describe("canonicalPortableMeetingName", () => {
  it("strips the bundle suffix from meeting artifact directories", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29.meeting")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });

  it("leaves plain directory names unchanged", () => {
    expect(canonicalPortableMeetingName("/tmp/daily-meeting-2026-03-12--12:29")).toBe(
      "daily-meeting-2026-03-12--12:29",
    );
  });
});

describe("listArtifactDirectories", () => {
  it("selects ready bundles and ignores unrelated directories", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-pack-discover-"));
    try {
      const readyDir = join(root, "a-ready.meeting");
      writeReadyBundleFixture(readyDir);
      mkdirSync(join(root, "b-unrelated"));

      expect(listArtifactDirectories(root)).toEqual([readyDir]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("rejects a bundle whose cassini.json state is not ready", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-pack-notready-"));
    try {
      const failedDir = join(root, "failed.meeting");
      writeReadyBundleFixture(failedDir, { state: "failed" });

      expect(isReadyMeetingBundle(failedDir)).toBe(false);
      expect(listArtifactDirectories(root)).toEqual([]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("rejects a bundle missing one of the files the Go packer requires", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-pack-partial-"));
    try {
      const partialDir = join(root, "partial.meeting");
      writeReadyBundleFixture(partialDir);
      rmSync(join(partialDir, "manifest.json"));

      expect(isReadyMeetingBundle(partialDir)).toBe(false);
      expect(listArtifactDirectories(root)).toEqual([]);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it("treats an unset bundle state as ready, like the Go packer does", () => {
    const root = mkdtempSync(join(tmpdir(), "cassini-pack-unset-"));
    try {
      const dir = join(root, "unset.meeting");
      writeReadyBundleFixture(dir);
      const manifestPath = join(dir, "cassini.json");
      const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
      delete manifest.state;
      writeFileSync(manifestPath, JSON.stringify(manifest));

      expect(isReadyMeetingBundle(dir)).toBe(true);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

describe("pack:portable-artifacts against the real cassini CLI", () => {
  // Proves the delegated command actually accepts what discovery selects:
  // builds the Go packer, discovers a real ready bundle, and lets the script
  // run `cassini pack` on it for real (no mocked runner).
  it.skipIf(!hasRealCliToolchain)(
    "packs a discovered ready bundle end-to-end",
    { timeout: 240_000 },
    () => {
      const root = mkdtempSync(join(tmpdir(), "cassini-pack-cli-"));
      try {
        const goRecorderDir = fileURLToPath(new URL("../../cassini-go-recorder/", import.meta.url));
        const cassiniBin = join(root, "cassini");
        execFileSync("go", ["build", "-o", cassiniBin, "./cmd/cassini"], {
          cwd: goRecorderDir,
          stdio: "pipe",
        });

        const sourceDir = join(root, "source");
        writeReadyBundleFixture(join(sourceDir, "demo.meeting"), { realAudio: true });
        const outputDir = join(root, "out");

        const scriptPath = fileURLToPath(new URL("./pack-portable-artifacts.mjs", import.meta.url));
        const stdout = execFileSync(
          process.execPath,
          [scriptPath, "--source-dir", sourceDir, "--output-dir", outputDir],
          { env: { ...process.env, CASSINI_BIN: cassiniBin }, encoding: "utf8" },
        );

        const packedPath = join(outputDir, "demo.opus");
        expect(stdout).toContain(`write ${packedPath}`);
        expect(existsSync(packedPath)).toBe(true);
        const packed = readFileSync(packedPath);
        expect(packed.length).toBeGreaterThan(0);
        // The packer commits a real Ogg Opus file, not a renamed input.
        expect(packed.subarray(0, 4).toString("ascii")).toBe("OggS");
      } finally {
        rmSync(root, { recursive: true, force: true });
      }
    },
  );
});
