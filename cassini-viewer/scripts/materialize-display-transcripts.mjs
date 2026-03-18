import { existsSync, mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { buildDisplayTranscriptFromArtifacts } from "./export-static-meetings.mjs";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main();
}

export function main(argv = process.argv.slice(2)) {
  const { sourceDir } = parseArgs(argv);
  const meetingDirs = findMeetingArtifactDirs(sourceDir);
  if (meetingDirs.length === 0) {
    throw new Error(`No meeting artifact directories found in ${sourceDir}`);
  }

  for (const meetingDir of meetingDirs) {
    materializeDisplayTranscriptForMeeting(meetingDir);
    console.log(`display transcript -> ${join(meetingDir, "transcript.display.v1.json")}`);
  }
}

export function parseArgs(argv) {
  let sourceDir = "";
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(viewerDir, next);
      index += 1;
    }
  }
  if (!sourceDir) {
    throw new Error("--source-dir is required");
  }
  return { sourceDir };
}

export function findMeetingArtifactDirs(sourceDir) {
  if (!existsSync(sourceDir)) {
    throw new Error(`Missing source directory: ${sourceDir}`);
  }

  const stats = statSync(sourceDir);
  if (!stats.isDirectory()) {
    throw new Error(`Source path must be a directory: ${sourceDir}`);
  }

  if (existsSync(join(sourceDir, "transcript.words.v1.json"))) {
    return [sourceDir];
  }

  return readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => join(sourceDir, entry.name))
    .filter((meetingDir) => existsSync(join(meetingDir, "transcript.words.v1.json")));
}

export function materializeDisplayTranscriptForMeeting(meetingDir) {
  const transcriptPath = join(meetingDir, "transcript.words.v1.json");
  const readablePath = join(meetingDir, "transcript.readable.v1.json");
  const transcript = JSON.parse(readFileSync(transcriptPath, "utf8"));
  const readable = existsSync(readablePath) ? JSON.parse(readFileSync(readablePath, "utf8")) : null;
  const display = buildDisplayTranscriptFromArtifacts(transcript, readable);

  mkdirSync(meetingDir, { recursive: true });
  writeFileSync(join(meetingDir, "transcript.display.v1.json"), `${JSON.stringify(display, null, 2)}\n`, "utf8");
  return display;
}
