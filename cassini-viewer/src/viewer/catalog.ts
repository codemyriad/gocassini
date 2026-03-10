export interface MeetingCatalogEntry {
  id: string;
  artifactPath: string;
  title: string;
  dateLabel: string;
  speakerCount: number;
  segmentCount: number;
  digestDurationMs: number;
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
  return validateMeetingCatalog((await response.json()) as unknown);
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
  const artifactPath = requireNonEmptyString(value.artifactPath, `catalog entry ${index} artifactPath`);
  const title = requireNonEmptyString(value.title, `catalog entry ${index} title`);
  const dateLabel = requireNonEmptyString(value.dateLabel, `catalog entry ${index} dateLabel`);

  return {
    id,
    artifactPath,
    title,
    dateLabel,
    speakerCount: requireNumber(value.speakerCount, `catalog entry ${index} speakerCount`),
    segmentCount: requireNumber(value.segmentCount, `catalog entry ${index} segmentCount`),
    digestDurationMs: requireNumber(value.digestDurationMs, `catalog entry ${index} digestDurationMs`),
  };
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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}
