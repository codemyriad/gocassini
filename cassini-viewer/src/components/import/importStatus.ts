// What the Import button beside search says while the sheet is closed: idle,
// imports still running, how many just finished (for a few seconds), or how
// many failed since the sheet was last open. A failure outranks the others.
export interface ImportStatus {
  kind: "idle" | "running" | "done" | "failed";
  count: number;
}
