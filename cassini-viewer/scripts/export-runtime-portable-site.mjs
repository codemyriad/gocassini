import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, extname, join, relative, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";

import {
  describeMeeting,
  readPortableMeeting,
  rewriteIndexHtmlForCatalog,
} from "./export-static-meetings.mjs";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
const distDir = resolve(viewerDir, "dist");

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
    throw new Error(`Missing portable meeting source directory: ${sourceDir}`);
  }

  const meetings = readdirSync(sourceDir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && extname(entry.name).toLowerCase() === ".opus")
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((entry) => {
      const meetingId = entry.name.slice(0, -extname(entry.name).length) || "meeting";
      readPortableMeeting(join(sourceDir, entry.name));
      const summary = describeMeeting(meetingId);
      return {
        id: meetingId,
        audioPath: toCatalogPath(relative(outputDir, join(sourceDir, entry.name))),
        title: summary.title,
        dateLabel: summary.dateLabel,
      };
    });

  if (meetings.length === 0) {
    throw new Error(`No .opus files found in ${sourceDir}`);
  }

  mkdirSync(outputDir, { recursive: true });
  const assetsDir = join(outputDir, "assets");
  rmSync(assetsDir, { recursive: true, force: true });
  cpSync(join(distDir, "assets"), assetsDir, { recursive: true });

  const builtIndexHtml = readFileSync(distIndexPath, "utf8");
  writeFileSync(join(outputDir, "index.html"), rewriteIndexHtmlForCatalog(builtIndexHtml), "utf8");
  writeFileSync(
    join(outputDir, "catalog.json"),
    `${JSON.stringify({ version: "cassini.viewer.catalog.v1", meetings }, null, 2)}\n`,
    "utf8",
  );

  console.log(`viewer index -> ${join(outputDir, "index.html")}`);
  console.log(`viewer catalog -> ${join(outputDir, "catalog.json")}`);
  for (const meeting of meetings) {
    console.log(`${meeting.id} -> ${meeting.audioPath}`);
  }
}

function parseArgs(argv) {
  let sourceDir = "";
  let outputDir = "";
  for (let index = 0; index < argv.length; index += 1) {
    if (argv[index] === "--source-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --source-dir");
      }
      sourceDir = resolve(viewerDir, next);
      index += 1;
      continue;
    }
    if (argv[index] === "--output-dir") {
      const next = argv[index + 1];
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = resolve(viewerDir, next);
      index += 1;
    }
  }

  if (!sourceDir) {
    throw new Error("--source-dir is required");
  }
  if (!outputDir) {
    outputDir = sourceDir;
  }
  return { sourceDir, outputDir };
}

function toCatalogPath(value) {
  const normalized = value.split(sep).join("/");
  if (normalized.startsWith("../") || normalized.startsWith("./")) {
    return normalized;
  }
  return `./${normalized}`;
}
