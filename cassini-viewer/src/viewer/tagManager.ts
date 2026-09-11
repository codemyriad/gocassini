import { writable } from "svelte/store";

import { AnnotationError, plural, type TagJob, type TagUpdate, type VocabularyTag } from "./annotations";
import type { DataProvider } from "./dataProvider";
import { colorFor, type TagColorId, type TagIconId } from "./tagPalette";

export type TagProvider = Pick<DataProvider, "updateTag" | "mergeTag" | "deleteTag" | "loadTagJob">;
export type TagAction =
  | { kind: "update"; tagId: string; update: TagUpdate }
  | { kind: "merge"; tagId: string; into: string }
  | { kind: "delete"; tagId: string };
// `lost`: the job vanished mid-run. Jobs live in the server's memory, so it restarted.
export type JobState = { job: TagJob | null; action: TagAction | null; lost: boolean; notice: string };
export type JobStatus = { text: string; failed: string[]; rerun: TagAction | null };

export const BUSY = "You already have a change running.";

export const countsLine = (tag: VocabularyTag) => `${plural(tag.meetings, "meeting")} · ${plural(tag.marks, "mark")}`;

export function confirmLine(meetings: number): string {
  if (meetings === 0) return "None of your meetings use it.";
  return `Updates ${meetings === 1 ? "one" : meetings} of your meetings. Meetings in rooms you can't open keep it.`;
}

const RELATIVE = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
const DAY = 864e5;
const UNITS = [["year", 365 * DAY], ["month", 30 * DAY], ["week", 7 * DAY], ["day", DAY], ["hour", 36e5], ["minute", 6e4]] as const;

export function relativeTime(iso: string, now = Date.now()): string {
  const ago = now - Date.parse(iso);
  const unit = UNITS.find(([, size]) => ago >= size);
  return unit ? RELATIVE.format(-Math.floor(ago / unit[1]), unit[0]) : "just now";
}

export function changedLine(tag: VocabularyTag, now = Date.now()): string {
  if (!tag.changedBy) return "";
  return `Changed by ${tag.changedBy}${tag.changedAtUtc ? ` · ${relativeTime(tag.changedAtUtc, now)}` : ""}`;
}

export function tagUpdate(tag: VocabularyTag, draft: { label: string; color: TagColorId; icon: TagIconId | "" }): TagUpdate | null {
  const update: TagUpdate = {};
  const label = draft.label.trim();
  if (label && label !== tag.label) update.label = label;
  if (draft.color !== colorFor(tag)) update.color = draft.color;
  if (draft.icon !== tag.icon) update.icon = draft.icon;
  return Object.keys(update).length > 0 ? update : null;
}

// A rename's job does not carry the new label, so only the action that started it can run it again.
function actionOf(job: TagJob): TagAction | null {
  if (job.kind === "merge" && job.into) return { kind: "merge", tagId: job.tagId, into: job.into };
  return job.kind === "delete" ? { kind: "delete", tagId: job.tagId } : null;
}

export function jobStatus({ job, action, lost }: JobState, tagId: string): JobStatus | null {
  if (!job || job.tagId !== tagId) return null;
  if (job.state === "running" && !lost) return { text: `Updating ${job.done} of ${job.total}…`, failed: [], rerun: null };
  const failed = job.failed.map((failure) => failure.meeting);
  const stopped = lost || job.state === "interrupted";
  if (!stopped && failed.length === 0) return null;
  return { text: stopped ? "Stopped before it finished." : `${failed.length} couldn't be updated`, failed, rerun: action ?? actionOf(job) };
}

async function send(provider: TagProvider, action: TagAction): Promise<TagJob | null> {
  if (action.kind === "update") return (await provider.updateTag!(action.tagId, action.update)).job;
  return action.kind === "merge" ? provider.mergeTag!(action.tagId, action.into) : provider.deleteTag!(action.tagId);
}

export function createJobTracker(provider: TagProvider, changed: () => void, interval = 1000) {
  let state: JobState = { job: null, action: null, lost: false, notice: "" };
  const store = writable(state);
  const set = (next: Partial<JobState>) => store.set((state = { ...state, ...next }));
  let timer: ReturnType<typeof setTimeout> | undefined;

  function follow(job: TagJob | null) {
    clearTimeout(timer);
    if (job?.state === "running") timer = setTimeout(poll, interval);
  }

  function adopt(job: TagJob) {
    set({ job, lost: false, action: job.id === state.job?.id ? state.action : null });
    follow(job);
  }

  async function poll() {
    const polling = state.job;
    const job = await provider.loadTagJob!().catch(() => undefined);
    if (state.job !== polling) return;
    if (job === undefined) return follow(state.job);
    if (job) adopt(job);
    else set({ lost: true });
    if (job?.state !== "running") changed();
  }

  async function refresh() {
    const job = await provider.loadTagJob!().catch(() => null);
    if (job?.state === "running") adopt(job);
  }

  async function run(action: TagAction): Promise<AnnotationError | null> {
    set({ notice: "" });
    try {
      const job = await send(provider, action);
      // A colour or icon starts no job, and must not stop following one that runs.
      if (job || state.job?.state !== "running" || state.lost) {
        set({ job, action, lost: false });
        follow(job);
      }
      changed();
      return null;
    } catch (caught) {
      const error = caught instanceof AnnotationError ? caught : new AnnotationError(0, "");
      if (error.code === "busy") void refresh();
      if (error.code !== "label-exists") set({ notice: error.code === "busy" ? BUSY : "That change didn't go through. Try again." });
      return error;
    }
  }

  return { subscribe: store.subscribe, run, refresh, stop: () => clearTimeout(timer) };
}
