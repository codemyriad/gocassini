export interface MeetingCatalogEntry {
  id: string;
  title: string;
  dateLabel: string;
  artifactPath?: string;
  audioPath?: string;
  speakerCount?: number;
  segmentCount?: number;
  digestDurationMs?: number;
}

export interface MeetingCatalog {
  version: "cassini.viewer.catalog.v1";
  meetings: MeetingCatalogEntry[];
}

const DEFAULT_CATALOG_PATH = "./catalog.json";
const CATALOG_FILE_NAME = "catalog.json";

export async function loadMeetingCatalog(path = DEFAULT_CATALOG_PATH): Promise<MeetingCatalog | null> {
  const targetUrl = path === DEFAULT_CATALOG_PATH ? resolveCatalogUrl() : path;
  const response = await fetch(targetUrl);
  if (!response.ok) {
    return null;
  }
  const catalog = validateMeetingCatalog((await response.json()) as unknown);
  return {
    ...catalog,
    meetings: sortMeetingCatalogEntries(
      catalog.meetings.map((meeting) => ({
        ...meeting,
        artifactPath: meeting.artifactPath
          ? resolveCatalogAssetUrl(meeting.artifactPath, response.url)
          : undefined,
        audioPath: meeting.audioPath ? resolveCatalogAssetUrl(meeting.audioPath, response.url) : undefined,
      })),
    ),
  };
}

export function validateMeetingCatalog(value: unknown): MeetingCatalog {
  if (!isRecord(value)) {
    throw new Error("catalog must be an object");
  }
  if (value.version !== "cassini.viewer.catalog.v1") {
    throw new Error("unsupported catalog version");
  }
  if (!Array.isArray(value.meetings)) {
    throw new Error("catalog meetings must be an array");
  }

  return {
    version: "cassini.viewer.catalog.v1",
    meetings: value.meetings.map((entry, index) => validateMeetingCatalogEntry(entry, index)),
  };
}

function validateMeetingCatalogEntry(value: unknown, index: number): MeetingCatalogEntry {
  if (!isRecord(value)) {
    throw new Error(`catalog entry ${index} must be an object`);
  }

  const id = requireNonEmptyString(value.id, `catalog entry ${index} id`);
  const title = requireNonEmptyString(value.title, `catalog entry ${index} title`);
  const dateLabel = requireNonEmptyString(value.dateLabel, `catalog entry ${index} dateLabel`);
  const artifactPath = optionalNonEmptyString(value.artifactPath, `catalog entry ${index} artifactPath`);
  const audioPath = optionalNonEmptyString(value.audioPath, `catalog entry ${index} audioPath`);
  if (!artifactPath && !audioPath) {
    throw new Error(`catalog entry ${index} must define artifactPath or audioPath`);
  }

  return {
    id,
    artifactPath,
    audioPath,
    title,
    dateLabel,
    speakerCount: optionalNumber(value.speakerCount, `catalog entry ${index} speakerCount`),
    segmentCount: optionalNumber(value.segmentCount, `catalog entry ${index} segmentCount`),
    digestDurationMs: optionalNumber(value.digestDurationMs, `catalog entry ${index} digestDurationMs`),
  };
}

export function sortMeetingCatalogEntries(meetings: MeetingCatalogEntry[]): MeetingCatalogEntry[] {
  return [...meetings].sort((left, right) => {
    const dateComparison = compareDescending(normalizeDateLabel(right.dateLabel), normalizeDateLabel(left.dateLabel));
    if (dateComparison !== 0) {
      return dateComparison;
    }
    return compareDescending(right.id, left.id);
  });
}

function requireNonEmptyString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${label} must be a non-empty string`);
  }
  return value;
}

function requireNumber(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new Error(`${label} must be a finite number`);
  }
  return value;
}

function optionalNonEmptyString(value: unknown, label: string): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  return requireNonEmptyString(value, label);
}

function optionalNumber(value: unknown, label: string): number | undefined {
  if (value === undefined) {
    return undefined;
  }
  return requireNumber(value, label);
}

function normalizeDateLabel(value: string): string {
  return value.trim();
}

function compareDescending(left: string, right: string): number {
  return left.localeCompare(right);
}

function resolveAppAssetUrl(assetPath: string): string {
  const base = import.meta.env.BASE_URL;
  const baseUrl = base && base !== "/" ? new URL(base, window.location.href) : new URL(window.location.href);
  return new URL(assetPath, baseUrl).toString();
}

// resolveCatalogUrl returns the URL the viewer should hit for catalog.json.
//
// Default behavior: resolve relative to the SPA's BASE_URL (Vite base), so a
// stand-alone exported site at https://example.com/foo/ fetches
// https://example.com/foo/catalog.json — what the existing portable export
// flow expects.
//
// When VITE_PUBLISHED_BASE is set at build time, the catalog (and by
// extension every artifactPath / audioPath the catalog points at) is served
// from a different URL prefix. The Nextcloud ExApp build sets this to
// `/published` so the viewer SPA can live at /viewer/ while the published
// archive stays at /published/.
function resolveCatalogUrl(): string {
  const publishedBase = readEnv("VITE_PUBLISHED_BASE");
  if (!publishedBase) {
    return resolveAppAssetUrl(DEFAULT_CATALOG_PATH);
  }
  const normalized = publishedBase.endsWith("/") ? publishedBase : `${publishedBase}/`;
  // A root-absolute VITE_PUBLISHED_BASE (the ExApp build sets `/published`)
  // bypasses the Nextcloud AppAPI proxy prefix: served at
  // /index.php/apps/app_api/proxy/<appid>/viewer/, an origin-absolute
  // /published/catalog.json 404s. Prepend the runtime proxy prefix (everything
  // before the /viewer mount) so the published archive resolves under the same
  // mount as the SPA. Standalone exports use a relative base and skip this.
  const withPrefix = normalized.startsWith("/")
    ? joinProxyPrefix(proxyPrefixFromPathname(window.location.pathname), normalized)
    : normalized;
  return new URL(CATALOG_FILE_NAME, new URL(withPrefix, window.location.href)).toString();
}

// proxyPrefixFromPathname derives the path prefix in front of the /viewer mount.
// Served directly by the operator the SPA lives at /viewer/, but through the
// AppAPI proxy it lives at /index.php/apps/app_api/proxy/<appid>/viewer/ —
// everything before /viewer is the prefix the published archive must share.
// Mirrors cassini-control-panel/src/operator/config.ts.
function proxyPrefixFromPathname(pathname: string): string {
  if (!pathname) {
    return "";
  }
  const marker = "/viewer";
  const idx = pathname.indexOf(`${marker}/`);
  if (idx > 0) {
    return pathname.slice(0, idx);
  }
  if (idx < 0 && pathname.endsWith(marker)) {
    return pathname.slice(0, pathname.length - marker.length);
  }
  return "";
}

function joinProxyPrefix(prefix: string, basePath: string): string {
  if (prefix === "") {
    return basePath;
  }
  return basePath === "/" ? prefix : prefix + basePath;
}

function readEnv(name: string): string {
  // import.meta.env exposes every VITE_* env baked in at build time.
  const value = (import.meta.env as Record<string, unknown> | undefined)?.[name];
  return typeof value === "string" ? value : "";
}

function resolveCatalogAssetUrl(assetPath: string, catalogUrl: string): string {
  return new URL(assetPath, catalogUrl).toString();
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
