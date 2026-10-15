// Topic suggestions (docs/topic-suggestions.md): the topic library people
// describe so Cassini can suggest topics for meetings. Every count the
// operator serves is over the meetings the caller can read.

import type { TagColorId, TagIconId } from "./tagPalette";

export interface TopicUsage {
  // Meetings the caller can read that carry the tag.
  meetings: number;
  // Suggestions waiting for review among those meetings.
  pending: number;
}

export interface Topic {
  tagId: string;
  label: string;
  definition: string;
  color: TagColorId | null;
  icon: TagIconId | null;
  status: "active" | "retired";
  mergedInto?: string;
  revision: number;
  updatedAt: string;
  // No vector for the definition yet: it is not suggested until it has one.
  preparing: boolean;
  // Pending or confirmed on most of the caller's checked meetings.
  broad: boolean;
  usage: TopicUsage;
}

export interface TopicLibrary {
  enabled: boolean;
  ready?: boolean;
  reason?: string;
  library?: Topic[];
}

// A create (no tagId) or an edit (tagId and expectRevision).
export interface TopicSave {
  tagId?: string;
  expectRevision?: number;
  label?: string;
  definition?: string;
  color?: TagColorId | "";
  icon?: TagIconId | "";
  status?: "active" | "retired";
  fromTagId?: string;
  separate?: boolean;
}

export interface TopicRevision {
  revision: number;
  kind: string;
  label?: string;
  definition?: string;
  at: string;
  redacted: boolean;
  canRedact: boolean;
}

// The rooms of the caller's meetings carrying a tag: describing a tag seen in
// one room publishes its name to everyone who can tag.
export interface TopicTagRooms {
  rooms: string[];
  roomCount: number;
}

export const DEFINITION_LIMIT = 1000;

export const TOPIC_NOTICE = "Everyone who can tag will see this name and definition. Do not paste text from a meeting.";

// newRequestId names one write, so a retry replays its first answer.
export function newRequestId(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(12));
  return "req_" + Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

// usageLine says how much of the caller's archive a topic touches.
export function usageLine(topic: Topic): string {
  const parts: string[] = [];
  parts.push(topic.usage.meetings === 1 ? "On 1 meeting" : `On ${topic.usage.meetings} meetings`);
  if (topic.usage.pending > 0) parts.push(`${topic.usage.pending} to review`);
  return parts.join(" · ");
}

// roomWarning is the editor's line before a tag's name goes to everyone, or
// "" when no warning is due. adopting: the topic takes an existing tag, by
// "Describe as topic" (fromTag) or because the typed name is one. Until the
// rooms are known, an adopted tag gets the general warning.
export function roomWarning(rooms: TopicTagRooms | null, fromTag: boolean, adopting = fromTag): string {
  if (!adopting) return "";
  if (!rooms) return "This keeps an existing tag. Describing it shows its name to everyone who can tag.";
  if (!fromTag && rooms.roomCount >= 2) return "";
  const named = rooms.rooms.length ? rooms.rooms.map((r) => `“${r}”`).join(", ") : "one conversation";
  if (rooms.roomCount < 2) return `This tag is only on meetings from ${named} that you can read. Describing it shows its name to everyone who can tag.`;
  return `This tag is on meetings from ${named}. Describing it shows its name to everyone who can tag.`;
}

// Review on the meeting page (S5).

export interface TopicEvidence {
  startMs: number;
  endMs: number;
  wordCount: number;
  // Counted toward the rule that made the suggestion.
  counted: boolean;
}

export interface TopicSuggestion {
  tagId: string;
  label: string;
  definition: string;
  color: TagColorId | null;
  icon: TagIconId | null;
  rule: "supporting-words" | "short-strong";
  supportWords: number;
  evidence: TopicEvidence[];
}

export interface TopicDecided {
  tagId: string;
  label: string;
  verdict: "confirmed" | "rejected";
  source: "review" | "direct";
  by?: string;
  at: string;
}

export interface MeetingSuggestions {
  enabled: boolean;
  // waiting | not-checked | done | unavailable | blocked
  state?: string;
  reason?: string;
  language?: string;
  validatedLanguage?: boolean;
  confirmable?: boolean;
  blockedReason?: string;
  suggestions?: TopicSuggestion[];
  decided?: TopicDecided[];
}

export type TopicDecision = "confirm" | "reject" | "undo" | "reopen";

export interface TopicCoverage {
  enabled?: boolean;
  visible: number;
  checked: number;
  waiting: number;
  notChecked: number;
  unavailable: Record<string, number>;
  blocked: number;
  checkedSince?: string;
  unvalidatedLanguage: number;
  unknownLanguage: number;
}

export const SUGGESTION_NOTE = "Suggested from the transcript. Not saved to the recording until someone confirms it.";
export const UNVALIDATED_NOTE = "Suggestions have not been checked for this meeting's language.";
export const UNRESOLVED_NOTE = "This meeting's audio changed since its tags were made. Its old tags need attention first, in the transcript, before a topic can be confirmed.";

// whyLine says, in words, why a topic was suggested.
export function whyLine(s: TopicSuggestion): string {
  if (s.rule === "short-strong") return "One short, focused discussion";
  const counted = s.evidence.filter((e) => e.counted);
  const ms = counted.reduce((sum, e) => sum + Math.max(0, e.endMs - e.startMs), 0);
  const minutes = Math.max(1, Math.round(ms / 60000));
  const passages = counted.length;
  return `Discussed for about ${minutes === 1 ? "a minute" : `${minutes} minutes`}${passages > 1 ? ` across ${passages} passages` : ""}`;
}

// meetingNote explains a meeting with no suggestions to review yet, or "".
export function meetingNote(view: MeetingSuggestions): string {
  switch (view.state) {
    case "waiting": return "Topic suggestions: this meeting is waiting to be checked.";
    case "not-checked": return "Topic suggestions: this meeting has not been checked.";
    case "unavailable": return `Topic suggestions: this meeting could not be checked (${reasonText(view.reason)}).`;
    case "blocked": return "Topic suggestions: checking this meeting failed; an administrator can retry it.";
    default: return "";
  }
}

const REASONS: Record<string, string> = {
  "no-transcript": "no transcript",
  "transcript-has-no-words": "the transcript is empty",
  "transcript-integrity": "the transcript is damaged",
  "recording-missing": "the recording is missing",
  "transcription-disabled": "transcription was off",
};
export const reasonText = (reason?: string) => REASONS[reason ?? ""] ?? "it could not be read";

// coverageLine is the one line of coverage over the meetings the caller can read.
export function coverageLine(c: TopicCoverage): string {
  const parts = [`Checked ${c.checked} of the ${c.visible} meetings you can see`];
  if (c.waiting) parts.push(`${c.waiting} waiting`);
  const unavailable = Object.values(c.unavailable).reduce((a, b) => a + b, 0);
  if (unavailable) {
    const reasons = Object.keys(c.unavailable).map(reasonText).join(", ");
    parts.push(`${unavailable} could not be checked (${reasons})`);
  }
  if (c.blocked) parts.push(`${c.blocked} failed`);
  if (c.checkedSince) {
    const since = new Date(c.checkedSince);
    const label = Number.isNaN(since.getTime()) ? c.checkedSince : since.toLocaleDateString(undefined, { day: "numeric", month: "short" });
    parts.push(`meetings before ${label} not checked yet`);
  } else if (c.notChecked) {
    parts.push(`${c.notChecked} not checked yet`);
  }
  if (c.unvalidatedLanguage) parts.push(`${c.unvalidatedLanguage} in languages not yet validated`);
  if (c.unknownLanguage) parts.push(`${c.unknownLanguage} language unknown`);
  return parts.join(" · ") + ".";
}

export const COVERAGE_LIMITS = "A missing suggestion does not mean the topic was absent: short mentions are missed, and speech-recognition errors carry through.";

// The cross-meeting review queue (S6).

export interface TopicQueueItem {
  meetingId: string;
  title: string;
  date: string;
  room?: string;
  rule: "supporting-words" | "short-strong";
  supportWords: number;
  quote?: string;
}

export interface TopicQueueGroup {
  tagId: string;
  label: string;
  color: TagColorId | null;
  icon: TagIconId | null;
  total: number;
  items: TopicQueueItem[];
}

export interface TopicQueue {
  enabled?: boolean;
  groups: TopicQueueGroup[];
  next: string;
  total: number;
  byMeeting: Record<string, number>;
  coverage: TopicCoverage;
}

export interface TopicQueueQuery {
  tag?: string;
  room?: string;
  cursor?: string;
  limit?: number;
}

export interface TopicBatchResult {
  meetingId: string;
  status: "confirmed" | "rejected" | "not-found" | "error";
  error?: string;
}

export const MAX_BATCH = 100;

// suggestedBadge is a list row's "2 suggested", or "".
export const suggestedBadge = (n: number | undefined) => (n ? `${n} suggested` : "");

// Recurring-topic proposals (S7).

export interface TopicProposal {
  id: string;
  roomId: string;
  room: string;
  state: "open" | "dismissed";
  // A draft from the meetings' own words: shown as such, never saved as is.
  draftName: string;
  hints: string[];
  flags: { routine?: boolean; edges?: boolean };
  nearest?: { tagId: string; label: string };
  meetings: number;
  days: number;
  evidence: { meetingId: string; title: string; date: string; startMs: number; endMs: number; quote?: string }[];
}

export type ProposalAction =
  | { action: "approve"; label: string; definition: string; color?: TagColorId; confirmTerms?: boolean }
  | { action: "link"; tagId: string }
  | { action: "dismiss" }
  | { action: "restore" };

// sharedTerms are the typed words that also occur in a draft name or its
// hints: words that came from the meetings (the operator checks the same).
export function sharedTerms(typed: string, draft: readonly string[]): string[] {
  const terms = (text: string) => (text.toLowerCase().match(/[\p{L}\p{N}\p{M}]{3,}/gu) ?? []);
  const from = new Set(draft.flatMap(terms));
  return [...new Set(terms(typed).filter((term) => from.has(term)))];
}
