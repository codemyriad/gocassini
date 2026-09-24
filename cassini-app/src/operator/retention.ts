export interface RetentionPolicy { forever: boolean; count?: number; unit?: "days" | "weeks" | "months" }
export interface RetentionGroup { mode: "group" | "fine"; fine_initialized?: boolean; policy: RetentionPolicy; fine: Record<string, RetentionPolicy> }
export interface RetentionSettings { version: number; revision: number; recordings: RetentionGroup; history: RetentionGroup; current: RetentionPolicy; logs: RetentionPolicy }
export const retentionLabels: Record<string, string> = {
  audio: "Captured audio", video: "Captured video", failed_capture: "Failed recordings",
  failed_build: "Failed build / seal output", superseded: "Superseded successful output", failed_publish: "Failed publish staging",
};
// Undefined fine policies are first-split drafts; persisted policies preserve inactive values.
export function changeRetentionMode(group: RetentionGroup, mode: "group" | "fine", firstSplit = false): RetentionGroup {
  return { ...group, mode, fine_initialized: group.fine_initialized || mode === "fine", fine: firstSplit ? Object.fromEntries(Object.keys(group.fine).map(k => [k, { ...group.policy }])) : group.fine };
}
