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

export async function loadMeetingCatalog(path = DEFAULT_CATALOG_PATH): Promise<MeetingCatalog | null> {
  const response = await fetch(path);
  if (!response.ok) {
    return null;
  }
  const catalog = validateMeetingCatalog((await response.json()) as unknown);
  return {
    ...catalog,
    meetings: sortMeetingCatalogEntries(catalog.meetings),
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
