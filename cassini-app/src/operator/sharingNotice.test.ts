import { describe, expect, it, vi } from "vitest";
import {
  acceptAllLabel,
  acceptShares,
  formatDuration,
  formatShareDate,
  hasSharingNews,
  pendingRowMeta,
  pendingRowTitle,
  pendingTitle,
  readAcceptSharesResult,
  refusedTitle,
  starterRefusedLine,
  starterRefusedTitle,
} from "./sharingNotice";
import type { PendingRecording, SharingStatus } from "./setupHealth";

const weekly: PendingRecording = {
  shareIds: ["34", "35"], jobId: "01K9QV7X3B2MA8ZC4D6N0RT5WE", title: "Weekly sync",
  startedAt: "2026-10-30T10:00:00Z", durationSeconds: 2520,
};
const checked = (over: Partial<SharingStatus> = {}): SharingStatus =>
  ({ checked: true, pending: [], refused: 0, refusedStarted: 0, ...over });

describe("whether Browse has anything to say about the reader's shares", () => {
  it("is quiet for an unchecked answer, an absent one, and zero counts", () => {
    expect(hasSharingNews(null)).toBe(false);
    expect(hasSharingNews(undefined)).toBe(false);
    expect(hasSharingNews(checked())).toBe(false);
    expect(hasSharingNews({ checked: false, pending: [], refused: 0, refusedStarted: 0 })).toBe(false);
  });

  it("speaks for a pending recording or a refused one", () => {
    expect(hasSharingNews(checked({ pending: [weekly] }))).toBe(true);
    expect(hasSharingNews(checked({ refused: 1 }))).toBe(true);
    expect(hasSharingNews(checked({ refusedStarted: 1 }))).toBe(true);
  });
});

describe("the copy", () => {
  it("counts recordings with their singulars", () => {
    expect(pendingTitle(3)).toBe("3 recordings are waiting for you");
    expect(pendingTitle(1)).toBe("1 recording is waiting for you");
    expect(acceptAllLabel(3)).toBe("Accept all 3");
    // One recording has nothing to say "all" about.
    expect(acceptAllLabel(1)).toBe("Accept");
    expect(refusedTitle(2)).toBe("2 recordings could not be shared with you.");
    expect(refusedTitle(1)).toBe("1 recording could not be shared with you.");
    expect(starterRefusedTitle(2)).toBe("2 recordings you started could not be published.");
    expect(starterRefusedTitle(1)).toBe("1 recording you started could not be published.");
  });

  it("keeps the explanation's pronouns in step with the count", () => {
    expect(starterRefusedLine(2)).toBe("Nextcloud refused to share them with you.");
    expect(starterRefusedLine(1)).toBe("Nextcloud refused to share it with you.");
  });
});

describe("a pending row", () => {
  const gb = { locale: "en-GB", timeZone: "UTC" };

  it("formats the date in the reader's locale and the duration in minutes", () => {
    expect(formatShareDate("2026-10-30T10:00:00Z", gb)).toBe("Fri 30 Oct, 10:00");
    expect(formatShareDate("2026-10-30T10:00:00Z", { locale: "en-US", timeZone: "UTC" })).toBe("Fri, Oct 30, 10:00 AM");
    expect(formatShareDate("", gb)).toBe("");
    expect(formatShareDate("not a date", gb)).toBe("");
    expect(formatDuration(2520)).toBe("42 min");
    expect(formatDuration(3900)).toBe("1 h 05");
    expect(formatDuration(7200)).toBe("2 h 00");
    expect(formatDuration(0)).toBe("");
    expect(formatDuration(20)).toBe("");
  });

  it("reads title, date and duration, and falls back when the job is unknown", () => {
    expect(pendingRowTitle(weekly, gb)).toBe("Weekly sync");
    expect(pendingRowMeta(weekly, gb)).toBe("Fri 30 Oct, 10:00 · 42 min");
    // No title: the date stands in for it, and is not repeated underneath.
    const untitled = { ...weekly, title: "" };
    expect(pendingRowTitle(untitled, gb)).toBe("Fri 30 Oct, 10:00");
    expect(pendingRowMeta(untitled, gb)).toBe("42 min");
    // Nothing known but the share itself.
    const bare = { shareIds: ["37"], jobId: "", title: "", startedAt: "", durationSeconds: 0 };
    expect(pendingRowTitle(bare, gb)).toBe("A recording");
    expect(pendingRowMeta(bare, gb)).toBe("");
  });
});

describe("POST operator/shares/accept", () => {
  it("sends the ids and reads what was accepted, what failed, and the refreshed shares", async () => {
    const fetchImpl = vi.fn(async () => Response.json({
      accepted: ["34"],
      failed: [{ share_id: "35", reason: "Wrong share ID, share does not exist" }],
      sharing: { checked: true, pending: [], refused: 0, refused_started: 0 },
    }));
    const result = await acceptShares("/operator/", ["34", "35"], fetchImpl as unknown as typeof fetch);
    expect(fetchImpl).toHaveBeenCalledWith("/operator/shares/accept", expect.objectContaining({
      method: "POST", body: JSON.stringify({ share_ids: ["34", "35"] }),
    }));
    expect(result).toEqual({
      accepted: ["34"],
      failed: [{ shareId: "35", reason: "Wrong share ID, share does not exist" }],
      sharing: { checked: true, pending: [], refused: 0, refusedStarted: 0 },
    });
  });

  it("asks for every pending share by name past the route's limit", async () => {
    const fetchImpl = vi.fn(async () => Response.json({ accepted: [], failed: [] }));
    await acceptShares("/operator", Array.from({ length: 101 }, (_, i) => String(i)), fetchImpl as unknown as typeof fetch);
    const [, init] = fetchImpl.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(String(init.body))).toEqual({ all: true });
  });

  it("throws the operator's own sentence for a refused call", async () => {
    const refused = vi.fn(async () => Response.json({ error: "Cassini could not read your pending shares." }, { status: 502 }));
    await expect(acceptShares("/operator", ["34"], refused as unknown as typeof fetch)).rejects.toThrow("Cassini could not read your pending shares.");
    const bare = vi.fn(async () => new Response("", { status: 503 }));
    await expect(acceptShares("/operator", ["34"], bare as unknown as typeof fetch)).rejects.toThrow("Could not accept the share (HTTP 503).");
  });

  it("tolerates an answer with nothing readable in it", () => {
    expect(readAcceptSharesResult(null)).toEqual({ accepted: [], failed: [], sharing: null });
    expect(readAcceptSharesResult({ accepted: [1], failed: [{ reason: "x" }, "y"], sharing: "none" }))
      .toEqual({ accepted: [], failed: [], sharing: null });
  });
});
