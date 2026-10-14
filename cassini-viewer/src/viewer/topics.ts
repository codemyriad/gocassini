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
