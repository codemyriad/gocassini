import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readdirSync, statSync } from "node:fs";
import { basename, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}

export async function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir } = parseArgs(argv);
  const artifactDirs = listArtifactDirectories(sourceDir);
  if (artifactDirs.length === 0) {
    throw new Error(`No artifact directories found in ${sourceDir}`);
  }

  mkdirSync(outputDir, { recursive: true });
  for (const artifactDir of artifactDirs) {
    const outputPath = join(outputDir, `${canonicalPortableMeetingName(artifactDir)}.opus`);
    const result = await packArtifactDirectory(artifactDir, outputPath);
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

export function listArtifactDirectories(sourceDir) {
  if (!existsSync(sourceDir)) {
    throw new Error(`Missing source directory: ${sourceDir}`);
  }
  if (!statSync(sourceDir).isDirectory()) {
    throw new Error(`Source path must be a directory: ${sourceDir}`);
  }
  return readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
    .map((entry) => join(sourceDir, entry.name))
    .filter((entry) => existsSync(join(entry, "transcript.words.v1.json")) && existsSync(join(entry, "meeting.opus")))
    .sort();
}

export async function packArtifactDirectory(artifactDir, outputPath, runCassini = execFileSync) {
  // Keep one portable producer. The Go packer owns the v3 manifest, canonical
  // compressed-Opus identity, and the post-remux integrity check. Reimplementing
  // those rules here previously left this maintenance command producing v1
  // exact-PCM files after the application had moved on.
  const cassiniBin = String(process.env.CASSINI_BIN ?? "cassini").trim() || "cassini";
  runCassini(cassiniBin, ["pack", artifactDir, "--out", outputPath], { stdio: "inherit" });
  return { status: "write" };
}
