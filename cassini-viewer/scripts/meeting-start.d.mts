// Types for meeting-start.mjs, which the viewer imports from TypeScript.

export type PortableMeetingStart =
  | { source: "recordedAtUtc" | "DATE"; value: string; recordedAtUtc: string }
  | { source: "recordedAtLocal"; value: string; recordedAtUtc?: undefined };

export function recordedAtUtcInstantMs(value: unknown): number | null;
export function rfc3339OffsetInstantMs(value: unknown): number | null;
export function portableMeetingStart(manifest: unknown, tags: unknown): PortableMeetingStart | null;
export function portableRecordedAtUtc(manifest: unknown, tags: unknown): string;
