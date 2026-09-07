// Pack every ready `.meeting` bundle in --source-dir into a portable `.opus`
// file in --output-dir by delegating to `cassini pack` (set CASSINI_BIN to
// point at the binary; default: `cassini` on PATH).
//
// A directory is packed only when it is a bundle `cassini pack` accepts — a
// `cassini.json` whose kind is "meeting" and whose state is ready (or unset),
// alongside `meeting.webm`, `transcript.words.v1.json`, and `manifest.json`.
//
// Discovery here is a shallow readiness screen so we never invoke the packer
// on a directory it categorically rejects; `cassini pack` remains the
// authority and still validates the bundle in depth (manifest `files` map,
// audio integrity, ...).
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync } from "node:fs";
import { basename, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir } = parseArgs(argv);
  const bundleDirs = listArtifactDirectories(sourceDir);
  if (bundleDirs.length === 0) {
    throw new Error(`No ready .meeting bundles found in ${sourceDir}`);
  }

  mkdirSync(outputDir, { recursive: true });
  for (const bundleDir of bundleDirs) {
    const outputPath = join(outputDir, `${canonicalPortableMeetingName(bundleDir)}.opus`);
    const result = await packArtifactDirectory(bundleDir, outputPath);
    console.log(`${result.status} ${outputPath}`);
  }
}

export function canonicalPortableMeetingName(artifactDir) {
  const name = basename(artifactDir);
  return name.endsWith(".meeting") ? name.slice(0, -".meeting".length) : name;
}

export function parseArgs(argv) {
  let outputDir = "";
  let sourceDir = "";
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(next);
      index += 1;
      continue;
    }
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(next);
      index += 1;
      continue;
    }
    throw new Error(`unknown argument: ${argv[index]}`);
  }

  if (!sourceDir) {
    throw new Error("--source-dir is required");
  }
  if (!outputDir) {
    throw new Error("--output-dir is required");
  }
  return { outputDir, sourceDir };
}

// Mirrors the Go packer's input gate (internal/cassini: LoadMeetingBundle,
// bundleIsReady, validateReadyMeetingBundleContents): a cassini.json of kind
// "meeting" whose state is "ready" (or unset, which the packer treats as
// ready), plus the three files every ready bundle carries. Deeper checks —
// the artifact manifest's `files` map naming only existing files — stay with
// `cassini pack` itself.
export function isReadyMeetingBundle(dir) {
  const manifestPath = join(dir, "cassini.json");
  if (!existsSync(manifestPath)) {
    return false;
  }
  let manifest;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch {
    return false;
  }
  if (String(manifest?.kind ?? "").toLowerCase() !== "meeting") {
    return false;
  }
  const state = String(manifest?.state ?? "").trim().toLowerCase();
  if (state !== "" && state !== "ready") {
    return false;
  }
  return ["meeting.webm", "transcript.words.v1.json", "manifest.json"].every((name) =>
    existsSync(join(dir, name)),
  );
}

export function listArtifactDirectories(sourceDir) {
  if (!existsSync(sourceDir)) {
    throw new Error(`Missing source directory: ${sourceDir}`);
  }
  if (!statSync(sourceDir).isDirectory()) {
    throw new Error(`Source path must be a directory: ${sourceDir}`);
  }
  const candidates = readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
    .map((entry) => join(sourceDir, entry.name))
    .sort();
  return candidates.filter((dir) => isReadyMeetingBundle(dir));
}

export async function packArtifactDirectory(artifactDir, outputPath, runCassini = execFileSync) {
  // Keep one portable producer. The Go packer owns the published manifest,
  // compressed-Opus identity, and the post-remux integrity check.
  const cassiniBin = String(process.env.CASSINI_BIN ?? "cassini").trim() || "cassini";
  runCassini(cassiniBin, ["pack", artifactDir, "--out", outputPath], { stdio: "inherit" });
  return { status: "write" };
}
