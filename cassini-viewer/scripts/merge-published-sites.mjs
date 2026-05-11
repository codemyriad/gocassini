import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const CATALOG_VERSION = "cassini.viewer.catalog.v1";
const SHELL_ENTRY_SKIP = new Set(["catalog.json", "meetings"]);

export function parseArgs(argv) {
  let primaryDir = "";
  let secondaryDir = "";
  let outputDir = "";
  let prefer = "second";

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    const next = argv[index + 1];
    if (arg === "--primary-dir") {
      if (!next) {
        throw new Error("missing value for --primary-dir");
      }
      primaryDir = next;
      index += 1;
      continue;
    }
    if (arg === "--secondary-dir") {
      if (!next) {
        throw new Error("missing value for --secondary-dir");
      }
      secondaryDir = next;
      index += 1;
      continue;
    }
    if (arg === "--output-dir") {
      if (!next) {
        throw new Error("missing value for --output-dir");
      }
      outputDir = next;
      index += 1;
      continue;
    }
    if (arg === "--prefer") {
      if (!next) {
        throw new Error("missing value for --prefer");
      }
      prefer = next;
      index += 1;
      continue;
    }
    if (arg === "--help") {
      return { help: true };
    }
    throw new Error(`unknown argument: ${arg}`);
  }

  if (!primaryDir || !secondaryDir || !outputDir) {
    throw new Error("--primary-dir, --secondary-dir, and --output-dir are required");
  }
  if (prefer !== "first" && prefer !== "second") {
    throw new Error(`--prefer must be "first" or "second", got ${prefer}`);
  }

  return {
    help: false,
    primaryDir: resolve(primaryDir),
    secondaryDir: resolve(secondaryDir),
    outputDir: resolve(outputDir),
    prefer,
  };
}

export function mergePublishedSites({ primaryDir, secondaryDir, outputDir, prefer = "second" }) {
  const primary = loadPublishedSite(primaryDir, "primary");
  const secondary = loadPublishedSite(secondaryDir, "secondary");
  const resolvedOutputDir = resolve(outputDir);
  if (resolvedOutputDir === primary.rootDir || resolvedOutputDir === secondary.rootDir) {
    throw new Error("output directory must differ from both source site directories");
  }
  const mergedCatalog = mergeCatalogs(primary.catalog, secondary.catalog, prefer);

  rmSync(resolvedOutputDir, { recursive: true, force: true });
  mkdirSync(resolvedOutputDir, { recursive: true });

  copySiteShell(primaryDir, resolvedOutputDir, { overwrite: true });
  copySiteShell(secondaryDir, resolvedOutputDir, { overwrite: false });

  const copyOrder = prefer === "second"
    ? [primary, secondary]
    : [secondary, primary];
  for (const site of copyOrder) {
    copyCatalogArtifacts(site.rootDir, site.catalog, resolvedOutputDir);
  }

  writeFileSync(join(resolvedOutputDir, "catalog.json"), `${JSON.stringify(mergedCatalog, null, 2)}\n`, "utf8");
  writeMergedSiteManifest({ primary, secondary, outputDir: resolvedOutputDir, meetingCount: mergedCatalog.meetings.length });

  return mergedCatalog;
}

export function mergeCatalogs(primaryCatalog, secondaryCatalog, prefer = "second") {
  const mergedOrder = [];
  const mergedById = new Map();

  for (const meeting of primaryCatalog.meetings) {
    mergedOrder.push(meeting.id);
    mergedById.set(meeting.id, structuredClone(meeting));
  }

  for (const meeting of secondaryCatalog.meetings) {
    if (!mergedById.has(meeting.id)) {
      mergedOrder.push(meeting.id);
      mergedById.set(meeting.id, structuredClone(meeting));
      continue;
    }
    if (prefer === "second") {
      mergedById.set(meeting.id, structuredClone(meeting));
    }
  }

  return {
    version: CATALOG_VERSION,
    meetings: mergedOrder.map((meetingId) => mergedById.get(meetingId)),
  };
}

export function loadPublishedSite(siteDir, label = "site") {
  const rootDir = resolve(siteDir);
  const catalogPath = join(rootDir, "catalog.json");
  if (!existsSync(catalogPath)) {
    throw new Error(`${label} site is missing catalog.json: ${rootDir}`);
  }
  return {
    rootDir,
    catalog: parseCatalog(readFileSync(catalogPath, "utf8"), `${label} catalog`),
    manifest: loadOptionalSiteManifest(rootDir),
  };
}

function parseCatalog(raw, label) {
  let catalog;
  try {
    catalog = JSON.parse(raw);
  } catch (error) {
    throw new Error(`could not parse ${label}: ${error}`);
  }
  if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) {
    throw new Error(`${label} must be an object`);
  }
  if (catalog.version !== CATALOG_VERSION) {
    throw new Error(`${label} version must be ${CATALOG_VERSION}`);
  }
  if (!Array.isArray(catalog.meetings)) {
    throw new Error(`${label} meetings must be an array`);
  }

  return {
    version: CATALOG_VERSION,
    meetings: catalog.meetings.map((meeting, index) => validateMeeting(meeting, `${label} meeting ${index}`)),
  };
}

function validateMeeting(meeting, label) {
  if (!meeting || typeof meeting !== "object" || Array.isArray(meeting)) {
    throw new Error(`${label} must be an object`);
  }
  if (typeof meeting.id !== "string" || meeting.id.trim() === "") {
    throw new Error(`${label} id must be a non-empty string`);
  }
  if (typeof meeting.title !== "string" || meeting.title.trim() === "") {
    throw new Error(`${label} title must be a non-empty string`);
  }
  if (typeof meeting.dateLabel !== "string" || meeting.dateLabel.trim() === "") {
    throw new Error(`${label} dateLabel must be a non-empty string`);
  }
  if (typeof meeting.artifactPath !== "string" && typeof meeting.audioPath !== "string") {
    throw new Error(`${label} must define artifactPath or audioPath`);
  }
  return meeting;
}

function copySiteShell(sourceDir, outputDir, { overwrite }) {
  for (const entry of readdirSync(sourceDir, { withFileTypes: true })) {
    if (SHELL_ENTRY_SKIP.has(entry.name)) {
      continue;
    }
    const sourcePath = join(sourceDir, entry.name);
    const targetPath = join(outputDir, entry.name);
    cpSync(sourcePath, targetPath, {
      recursive: true,
      force: overwrite,
      errorOnExist: false,
    });
  }
}

function copyCatalogArtifacts(siteDir, catalog, outputDir) {
  const copiedPaths = new Set();
  for (const meeting of catalog.meetings) {
    for (const assetPath of [meeting.artifactPath, meeting.audioPath]) {
      if (typeof assetPath !== "string" || assetPath.trim() === "") {
        continue;
      }
      const sourcePath = resolveSiteAssetPath(siteDir, assetPath);
      const targetPath = join(outputDir, normalizeSiteAssetPath(assetPath));
      if (copiedPaths.has(targetPath)) {
        continue;
      }
      copiedPaths.add(targetPath);
      rmSync(targetPath, { recursive: true, force: true });
      mkdirSync(dirname(targetPath), { recursive: true });
      cpSync(sourcePath, targetPath, { recursive: true, force: true });
    }
  }
}

export function normalizeSiteAssetPath(assetPath) {
  const normalized = String(assetPath).trim().replace(/^\.\//, "").replace(/^\/+/, "");
  if (normalized === "") {
    throw new Error("asset path must not be empty");
  }
  return normalized;
}

export function resolveSiteAssetPath(siteDir, assetPath) {
  const raw = String(assetPath).trim();
  if (/^[a-zA-Z][a-zA-Z0-9+.-]*:/.test(raw) || raw.startsWith("//")) {
    throw new Error(`external asset paths are not supported: ${assetPath}`);
  }
  const normalized = normalizeSiteAssetPath(assetPath);
  const resolvedPath = resolve(siteDir, normalized);
  const rel = relative(siteDir, resolvedPath);
  if (rel === ".." || rel.startsWith(`..${process.platform === "win32" ? "\\" : "/"}`)) {
    throw new Error(`asset path escapes site root: ${assetPath}`);
  }
  if (!existsSync(resolvedPath)) {
    throw new Error(`site asset is missing: ${resolvedPath}`);
  }
  return resolvedPath;
}

function loadOptionalSiteManifest(siteDir) {
  const manifestPath = join(siteDir, "cassini.json");
  if (!existsSync(manifestPath)) {
    return null;
  }
  try {
    const parsed = JSON.parse(readFileSync(manifestPath, "utf8"));
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

function writeMergedSiteManifest({ primary, secondary, outputDir, meetingCount }) {
  const template = primary.manifest ?? secondary.manifest;
  if (!template || String(template.kind ?? "").toLowerCase() !== "site") {
    return;
  }

  const merged = {
    ...template,
    state: template.state ?? "ready",
    stage: template.stage ?? "ready",
    error: "",
    source_path: `${primary.rootDir} + ${secondary.rootDir}`,
    catalog_path: "catalog.json",
    meeting_count: meetingCount,
  };
  writeFileSync(join(outputDir, "cassini.json"), `${JSON.stringify(merged, null, 2)}\n`, "utf8");
}

function usage() {
  return `Usage:
  node ./scripts/merge-published-sites.mjs \
    --primary-dir /path/to/site-a \
    --secondary-dir /path/to/site-b \
    --output-dir /path/to/merged-site \
    [--prefer first|second]\n`;
}

async function main(argv) {
  try {
    const options = parseArgs(argv);
    if (options.help) {
      console.log(usage());
      return;
    }
    const mergedCatalog = mergePublishedSites(options);
    console.log(`merged published site -> ${options.outputDir}`);
    console.log(`meetings -> ${mergedCatalog.meetings.length}`);
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  await main(process.argv.slice(2));
}
