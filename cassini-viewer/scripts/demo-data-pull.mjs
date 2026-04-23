import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(scriptDir, "..");
export const defaultDemoDataDir = resolve(viewerDir, "exports", "viewer-demo");
const assetReferencePattern = /<(?:script|link)\b[^>]+(?:src|href)=["']([^"']+)["']/gi;

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}

export async function main(argv = process.argv.slice(2), options = {}) {
  const { fetchImpl = fetch, env = process.env, log = console.log } = options;
  const { outputDir } = parseArgs(argv);
  const baseUrl = normalizeDemoBaseUrl(env.DEMO_DATA_URL);

  if (!baseUrl) {
    throw new Error("DEMO_DATA_URL is not set");
  }

  prepareOutputDir(outputDir);

  const indexHtml = await downloadRequired(resolveUrl(baseUrl, "index.html"), fetchImpl);
  writeDownloadedFile(outputDir, "index.html", indexHtml);
  log(`[1/3] Downloaded site shell -> ${join(outputDir, "index.html")}`);

  for (const assetPath of extractAssetPaths(indexHtml)) {
    const body = await downloadRequired(resolveUrl(baseUrl, assetPath), fetchImpl);
    writeDownloadedFile(outputDir, assetPath, body);
  }

  const catalogRaw = await downloadRequired(resolveUrl(baseUrl, "catalog.json"), fetchImpl);
  writeDownloadedFile(outputDir, "catalog.json", catalogRaw);
  const catalog = JSON.parse(catalogRaw);
  validateCatalog(catalog);
  log(`[2/3] Downloaded catalog (${catalog.meetings.length} meetings)`);

  for (const meeting of catalog.meetings) {
    await pullMeeting(outputDir, baseUrl, meeting, fetchImpl);
  }

  log(`[3/3] Downloaded meeting artifacts -> ${join(outputDir, "meetings")}`);
}

export function parseArgs(argv) {
  let outputDir = defaultDemoDataDir;
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
    throw new Error(`unknown argument: ${argv[index]}`);
  }
  return { outputDir };
}

export function normalizeDemoBaseUrl(value) {
  if (typeof value !== "string" || value.trim() === "") {
    return "";
  }
  const url = new URL(value.trim());
  url.search = "";
  url.hash = "";
  if (!url.pathname.endsWith("/")) {
    url.pathname += "/";
  }
  return url.toString();
}

export function extractAssetPaths(indexHtml) {
  const assets = [];
  const seen = new Set();
  for (const match of indexHtml.matchAll(assetReferencePattern)) {
    const raw = match[1]?.trim() ?? "";
    if (!raw || raw.startsWith("http://") || raw.startsWith("https://")) {
      continue;
    }
    const normalized = raw.replace(/^\.\//, "").replace(/^\//, "");
    if (!normalized.startsWith("assets/") || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    assets.push(normalized);
  }
  return assets.sort((left, right) => left.localeCompare(right));
}

export function requiredManifestFiles(files) {
  const requiredKeys = ["audio", "transcript", "display_transcript", "readable_transcript"];
  return requiredKeys.map((key) => {
    const value = files?.[key];
    if (typeof value !== "string" || value.trim() === "") {
      throw new Error(`manifest is missing files.${key}`);
    }
    return value;
  });
}

export function prepareOutputDir(outputDir) {
  rmSync(outputDir, { recursive: true, force: true });
  mkdirSync(outputDir, { recursive: true });
}

async function pullMeeting(outputDir, baseUrl, meeting, fetchImpl) {
  if (typeof meeting?.artifactPath !== "string" || meeting.artifactPath.trim() === "") {
    throw new Error(`meeting ${meeting?.id ?? "unknown"} is missing artifactPath`);
  }
  const artifactPath = meeting.artifactPath.replace(/^\.\//, "").replace(/^\//, "");
  const artifactBaseUrl = resolveUrl(baseUrl, `${artifactPath}/`);
  const manifestRaw = await downloadRequired(resolveUrl(artifactBaseUrl, "manifest.json"), fetchImpl);
  writeDownloadedFile(outputDir, `${artifactPath}/manifest.json`, manifestRaw);
  const manifest = JSON.parse(manifestRaw);

  for (const fileName of requiredManifestFiles(manifest.files)) {
    const body = await downloadRequired(resolveUrl(artifactBaseUrl, fileName), fetchImpl);
    writeDownloadedFile(outputDir, `${artifactPath}/${fileName}`, body);
  }

  for (const optionalFile of ["summary.md", "captions.vtt", "chapters.vtt"]) {
    const body = await downloadOptional(resolveUrl(artifactBaseUrl, optionalFile), fetchImpl);
    if (body === null) {
      continue;
    }
    writeDownloadedFile(outputDir, `${artifactPath}/${optionalFile}`, body);
  }
}

async function downloadRequired(url, fetchImpl) {
  const response = await fetchImpl(url);
  if (!response.ok) {
    throw new Error(`download failed for ${url}: HTTP ${response.status}`);
  }
  return await response.text();
}

async function downloadOptional(url, fetchImpl) {
  const response = await fetchImpl(url);
  if (response.status === 404) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`download failed for ${url}: HTTP ${response.status}`);
  }
  return await response.text();
}

function validateCatalog(catalog) {
  if (catalog?.version !== "cassini.viewer.catalog.v1") {
    throw new Error("catalog version must be cassini.viewer.catalog.v1");
  }
  if (!Array.isArray(catalog.meetings)) {
    throw new Error("catalog meetings must be an array");
  }
}

function resolveUrl(baseUrl, relativePath) {
  return new URL(relativePath, baseUrl).toString();
}

function writeDownloadedFile(outputDir, relativePath, body) {
  const targetPath = join(outputDir, relativePath);
  mkdirSync(dirname(targetPath), { recursive: true });
  writeFileSync(targetPath, body, "utf8");
}
