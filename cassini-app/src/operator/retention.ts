import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";

export const retentionDayPresets = [7, 30, 60, 90] as const;
export interface RetentionPolicy { forever: boolean; count?: number; unit?: "days" }
export interface RetentionGroup { mode: "group" | "fine"; fine_initialized?: boolean; policy: RetentionPolicy; fine: Record<string, RetentionPolicy> }
export interface RetentionSettings { nextcloud?: {meetings:RetentionPolicy}; version: number; revision: number; schedule: { time: string; timezone: string }; recordings: RetentionPolicy; history: RetentionGroup; current: RetentionPolicy; logs: RetentionPolicy }
// The name of a kind of history the operator can keep for its own time. Read
// at call time so it follows the reader's language; a kind this build does not
// know shows as the operator sent it.
export function retentionLabel(kind: string): string {
  const t = get(LL).app.settings.retention.kinds;
  switch (kind) {
    case "failed_capture": return t.failed_capture();
    case "failed_build": return t.failed_build();
    case "superseded": return t.superseded();
    case "failed_publish": return t.failed_publish();
    default: return kind;
  }
}
// Undefined fine policies are first-split drafts; persisted policies preserve inactive values.
export function changeRetentionMode(group: RetentionGroup, mode: "group" | "fine", firstSplit = false): RetentionGroup {
  return { ...group, mode, fine_initialized: group.fine_initialized || mode === "fine", fine: firstSplit ? Object.fromEntries(Object.keys(group.fine).map(k => [k, { ...group.policy }])) : group.fine };
}

export interface RetentionOperations { operations: {name:string;status:string;error:string;updatedAt:string}[]; offset:number; nextOffset:number; historyNotice:string }
export interface RetentionPreview {usage:{count:number;bytes:number};now:string; revision:number; capability:boolean; reason?:string; historyNotice:string; retire:number; meetings:{name:string;createdAtUtc:string;recordedAtLocal:string;age:number|null;decision:"evict"|"keep";action:string;deadline?:string;reason?:string}[]}
