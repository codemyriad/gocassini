import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
const distDir = resolve(viewerDir, "dist");
const defaultSourceDir = resolve(viewerDir, "public", "demo");
const defaultOutputDir = resolve(viewerDir, "exports", "static-meetings");

const { outputDir, sourceDir } = parseArgs(process.argv.slice(2));

if (!existsSync(join(distDir, "index.html"))) {
  throw new Error(`Missing ${join(distDir, "index.html")}. Run "npm run build" first.`);
}
if (!existsSync(sourceDir)) {
  throw new Error(
    `Missing meeting source directory: ${sourceDir}. Pass --source-dir <artifact-root> when artifacts are stored outside this repo.`,
  );
}

const builtIndexHtml = readFileSync(join(distDir, "index.html"), "utf8");
const exportedIndexHtml = rewriteIndexHtmlForBundle(builtIndexHtml);
const meetingDirs = readdirSync(sourceDir, { withFileTypes: true })
  .filter((entry) => entry.isDirectory())
  .map((entry) => entry.name)
  .sort();

if (meetingDirs.length === 0) {
  throw new Error(`No meeting directories found in ${sourceDir}.`);
}

rmSync(outputDir, { recursive: true, force: true });
mkdirSync(outputDir, { recursive: true });

const exports = meetingDirs.map((meetingId) => exportMeeting(meetingId, exportedIndexHtml));
writeFileSync(join(outputDir, "index.html"), renderLandingPage(exports), "utf8");

for (const artifact of exports) {
  console.log(`${artifact.id} -> ${artifact.outputDir}`);
}
console.log(`landing page -> ${join(outputDir, "index.html")}`);

function parseArgs(argv) {
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

function rewriteIndexHtmlForBundle(indexHtml) {
  return indexHtml
    .replace(/(src|href)="\/assets\//g, '$1="./assets/')
    .replace(
      "</head>",
      '    <script>window.__CASSINI_VIEWER_ARTIFACT_MODE__ = "bundled";</script>\n  </head>',
    );
}

function exportMeeting(meetingId, indexHtml) {
  const sourceMeetingDir = join(sourceDir, meetingId);
  const targetDir = join(outputDir, meetingId);
  const manifest = readManifest(sourceMeetingDir, meetingId);

  mkdirSync(targetDir, { recursive: true });
  cpSync(join(distDir, "assets"), join(targetDir, "assets"), { recursive: true });
  cpSync(sourceMeetingDir, targetDir, { recursive: true });
  writeFileSync(join(targetDir, "index.html"), indexHtml, "utf8");

  return {
    id: meetingId,
    outputDir: targetDir,
    speakerCount: manifest.speakerCount ?? 0,
    segmentCount: manifest.segmentCount ?? 0,
    digestDurationMs: manifest.digestDurationMs ?? 0,
    title: formatMeetingTitle(meetingId),
  };
}

function readManifest(sourceDir, meetingId) {
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

function formatMeetingTitle(meetingId) {
  const match = /^(.*)--(\d{4})-(\d{2})-(\d{2})--(\d{2})-(\d{2})-(\d{2})$/.exec(meetingId);
  if (!match) {
    return meetingId;
  }
  const [, rawTitle, year, month, day, hour, minute] = match;
  return `${toTitleCase(rawTitle)} - ${year}-${month}-${day} ${hour}:${minute}`;
}

function toTitleCase(text) {
  return text
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function formatDuration(ms) {
  const totalSeconds = Math.max(0, Math.round(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

function renderLandingPage(exports) {
  const items = exports
    .map(
      (artifact) => `      <li>
        <a href="./${artifact.id}/">${escapeHtml(artifact.title)}</a>
        <span>${artifact.speakerCount} speakers</span>
        <span>${artifact.segmentCount} segments</span>
        <span>${formatDuration(artifact.digestDurationMs)}</span>
      </li>`,
    )
    .join("\n");

  return `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Cassini Meetings</title>
    <style>
      :root {
        color-scheme: light;
        font-family: Georgia, "Times New Roman", serif;
      }
      body {
        margin: 0;
        min-height: 100vh;
        background: linear-gradient(180deg, #f5efe3 0%, #ebe4d6 100%);
        color: #251d14;
      }
      main {
        max-width: 960px;
        margin: 0 auto;
        padding: 3rem 1rem 4rem;
      }
      h1 {
        margin: 0 0 0.75rem;
        font-size: clamp(2rem, 5vw, 3.6rem);
      }
      p {
        max-width: 60ch;
        line-height: 1.5;
      }
      ul {
        list-style: none;
        padding: 0;
        margin: 2rem 0 0;
        display: grid;
        gap: 0.9rem;
      }
      li {
        display: grid;
        gap: 0.2rem;
        padding: 1rem 1.1rem;
        border: 1px solid rgba(37, 29, 20, 0.15);
        border-radius: 0.9rem;
        background: rgba(255, 252, 246, 0.78);
      }
      a {
        color: inherit;
        font-size: 1.15rem;
        font-weight: 700;
        text-decoration: none;
      }
      a:hover {
        text-decoration: underline;
      }
      span {
        color: #5d4f3b;
      }
    </style>
  </head>
  <body>
    <main>
      <h1>Cassini Meetings</h1>
      <p>Each directory below is a standalone static package. Upload this whole folder to a web server and open any meeting in a browser.</p>
      <ul>
${items}
      </ul>
    </main>
  </body>
</html>
`;
}

function escapeHtml(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
