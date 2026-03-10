import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const CATALOG_VERSION = "cassini.viewer.catalog.v1";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
const distDir = resolve(viewerDir, "dist");
const defaultSourceDir = resolve(viewerDir, "public", "demo");
const defaultOutputDir = resolve(viewerDir, "exports", "static-meetings");

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { outputDir, sourceDir } = parseArgs(argv);
  const distIndexPath = join(distDir, "index.html");

  if (!existsSync(distIndexPath)) {
    throw new Error(`Missing ${distIndexPath}. Run "npm run build" first.`);
  }
  if (!existsSync(sourceDir)) {
    throw new Error(
      `Missing meeting source directory: ${sourceDir}. Pass --source-dir <artifact-root> when artifacts are stored outside this repo.`,
    );
  }

  const meetingDirs = readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name)
    .sort();

  if (meetingDirs.length === 0) {
    throw new Error(`No meeting directories found in ${sourceDir}.`);
  }

  rmSync(outputDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
  mkdirSync(join(outputDir, "meetings"), { recursive: true });

  const builtIndexHtml = readFileSync(distIndexPath, "utf8");
  writeFileSync(join(outputDir, "index.html"), rewriteIndexHtmlForCatalog(builtIndexHtml), "utf8");
  cpSync(join(distDir, "assets"), join(outputDir, "assets"), { recursive: true });

  const meetings = meetingDirs.map((meetingId) => exportMeeting({ meetingId, sourceDir, outputDir }));
  writeFileSync(
    join(outputDir, "catalog.json"),
    `${JSON.stringify({ version: CATALOG_VERSION, meetings }, null, 2)}\n`,
    "utf8",
  );

  console.log(`viewer index -> ${join(outputDir, "index.html")}`);
  console.log(`viewer catalog -> ${join(outputDir, "catalog.json")}`);
  for (const meeting of meetings) {
    console.log(`${meeting.id} -> ${join(outputDir, "meetings", meeting.id)}`);
  }
}

export function parseArgs(argv) {
  let outputDir = defaultOutputDir;
  let sourceDir = defaultSourceDir;
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(viewerDir, next);
      index += 1;
    }
  }
  return { outputDir, sourceDir };
}

export function rewriteIndexHtmlForCatalog(indexHtml) {
  return indexHtml.replace(/(src|href)="\/assets\//g, '$1="./assets/');
}

export function exportMeeting({ meetingId, sourceDir, outputDir }) {
  const sourceMeetingDir = join(sourceDir, meetingId);
  const targetMeetingDir = join(outputDir, "meetings", meetingId);
  const manifest = readManifest(sourceMeetingDir, meetingId);
  const { title, dateLabel } = describeMeeting(meetingId);

  mkdirSync(targetMeetingDir, { recursive: true });
  copyPublicMeetingFiles(sourceMeetingDir, targetMeetingDir, manifest);

  return {
    id: meetingId,
    artifactPath: `./meetings/${meetingId}`,
    title,
    dateLabel,
    speakerCount: manifest.speakerCount ?? 0,
    segmentCount: manifest.segmentCount ?? 0,
    digestDurationMs: manifest.digestDurationMs ?? 0,
  };
}

export function readManifest(sourceDir, meetingId) {
  const manifestPath = join(sourceDir, "manifest.json");
  if (!existsSync(manifestPath)) {
    return {};
  }
  try {
    return JSON.parse(readFileSync(manifestPath, "utf8"));
  } catch (error) {
    throw new Error(`could not parse manifest for ${meetingId}: ${error}`);
  }
}

export function copyPublicMeetingFiles(sourceMeetingDir, targetMeetingDir, manifest) {
  const filesToCopy = new Set([
    "manifest.json",
    "meeting.webm",
    "transcript.words.v1.json",
    "transcript.readable.v1.json",
    "captions.vtt",
    "chapters.vtt",
    "timeline.map.v1.json",
  ]);

  if (manifest && typeof manifest === "object" && manifest.files && typeof manifest.files === "object") {
    for (const value of Object.values(manifest.files)) {
      if (typeof value === "string" && value.trim() !== "") {
        filesToCopy.add(value);
      }
    }
  }

  for (const optionalFile of ["transcript.readable.v1.json", "chapters.vtt"]) {
    const optionalPath = join(sourceMeetingDir, optionalFile);
    if (existsSync(optionalPath)) {
      filesToCopy.add(optionalFile);
    }
  }

  for (const relativePath of filesToCopy) {
    const sourcePath = join(sourceMeetingDir, relativePath);
    if (!existsSync(sourcePath)) {
      continue;
    }
    const targetPath = join(targetMeetingDir, relativePath);
    mkdirSync(dirname(targetPath), { recursive: true });
    cpSync(sourcePath, targetPath, { recursive: true });
  }
}

export function describeMeeting(meetingId) {
  const colonTimeStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2}):(\d{2})(?::(\d{2}))?$/.exec(meetingId);
  if (colonTimeStamp) {
    const [, rawTitle, year, month, day, hour, minute] = colonTimeStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  const modernStamp = /^(.*)--(\d{8})T(\d{2})(\d{2})(\d{2})$/.exec(meetingId);
  if (modernStamp) {
    const [, rawTitle, yyyymmdd, hour, minute] = modernStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${yyyymmdd.slice(0, 4)}-${yyyymmdd.slice(4, 6)}-${yyyymmdd.slice(6, 8)} ${hour}:${minute}`,
    };
  }

  const legacyStamp = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(meetingId);
  if (legacyStamp) {
    const [, rawTitle, year, month, day, hour, minute] = legacyStamp;
    return {
      title: toTitleCase(rawTitle),
      dateLabel: `${year}-${month}-${day} ${hour}:${minute}`,
    };
  }

  return {
    title: toTitleCase(meetingId),
    dateLabel: meetingId,
  };
}

export function toTitleCase(text) {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
