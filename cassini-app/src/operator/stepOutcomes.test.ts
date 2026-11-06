import { describe, expect, it } from "vitest";
import { describeRefused, listRefusedIds, outcomeAttempt, reshareOutcome, shareNotice, summaryNotice, transcriptionNotice } from "./stepOutcomes";
import type { JobAttempt, JobDetailResponse } from "./types";

const attempt = (n: number, outcomes: Partial<JobAttempt> = {}): JobAttempt =>
  ({ attempt_number: n, ...outcomes }) as JobAttempt;

describe("outcomeAttempt", () => {
  it("shows the published attempt", () => {
    const detail = {
      availability: { source: "present", output: "present", published_attempt: 1 },
      attempts: [attempt(2, { transcription_status: "completed" }), attempt(1, { transcription_status: "failed" })],
    } as JobDetailResponse;
    expect(outcomeAttempt(detail)?.attempt_number).toBe(1);
  });

  it("falls back to the newest attempt with outcomes before anything is published", () => {
    const detail = { attempts: [attempt(3), attempt(2, { summary_status: "failed" }), attempt(1, { summary_status: "completed" })] } as JobDetailResponse;
    expect(outcomeAttempt(detail)?.attempt_number).toBe(2);
    expect(outcomeAttempt({ attempts: [attempt(1)] } as JobDetailResponse)).toBeNull();
  });
});

describe("transcriptionNotice", () => {
  it("says nothing for a transcribed meeting or an unknown one", () => {
    expect(transcriptionNotice(attempt(1, { transcription_status: "completed" }), true)).toBeNull();
    expect(transcriptionNotice(attempt(1), true)).toBeNull();
  });

  it("names a crash, keeps the operator's detail and offers a rerun", () => {
    const notice = transcriptionNotice(
      attempt(1, { transcription_status: "failed", transcription_reason: "transcription_failed", transcription_detail: "CUDA error: out of memory" }),
      true,
    );
    expect(notice).toEqual({
      tone: "error",
      title: "Published without a transcript: transcription failed",
      detail: "CUDA error: out of memory",
      hint: "Rerun transcribes it again.",
    });
  });

  it("names resources that never freed, and offers no rerun that cannot run", () => {
    const notice = transcriptionNotice(attempt(1, { transcription_status: "failed", transcription_reason: "insufficient_resources" }), false);
    expect(notice?.title).toBe("Published without a transcript: not enough memory or GPU to transcribe");
    expect(notice?.hint).toBeUndefined();
  });

  it("tells a model problem and a switched-off step from a failure", () => {
    expect(transcriptionNotice(attempt(1, { transcription_status: "skipped", transcription_reason: "model_unavailable" }), true)?.tone).toBe("warning");
    expect(transcriptionNotice(attempt(1, { transcription_status: "skipped", transcription_reason: "disabled" }), true)).toEqual({
      tone: "info",
      title: "Published without a transcript: transcription is turned off",
    });
  });
});

describe("summaryNotice", () => {
  it("says nothing for a written summary, an unknown one, or a meeting with no transcript", () => {
    expect(summaryNotice(attempt(1, { summary_status: "completed" }))).toBeNull();
    expect(summaryNotice(attempt(1))).toBeNull();
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "no_transcript" }))).toBeNull();
  });

  it("tells a failed summary from one that is off or not set up", () => {
    expect(summaryNotice(attempt(1, { summary_status: "failed", summary_reason: "llm_failed" }))?.tone).toBe("error");
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "disabled" }))).toEqual({
      tone: "info",
      title: "No summary: summaries are turned off",
    });
    expect(summaryNotice(attempt(1, { summary_status: "skipped", summary_reason: "not_configured" }))?.hint).toBe("Add one in Settings.");
  });

  it("warns when the built-in wrote the summary in place of the configured template (D-819)", () => {
    const notice = summaryNotice(attempt(1, { summary_status: "completed", summary_reason: "template_fallback" }));
    expect(notice?.tone).toBe("warning");
    expect(notice?.title).toContain("could not run");
  });
});

describe("shareNotice", () => {
  it("says nothing where everyone was given the meeting, or nothing was recorded", () => {
    expect(shareNotice(undefined)).toBeNull();
    expect(shareNotice({ status: "complete" })).toBeNull();
    expect(shareNotice({ status: "partial", skipped: [] })).toBeNull();
  });

  it("names who a published meeting could not be shared with, behind a disclosure", () => {
    const notice = shareNotice({
      status: "partial",
      skipped: [
        { type: "user", id: "bob", reason: "Sharing is only allowed with group members" },
        { type: "group", id: "marketing", reason: "Sharing is only allowed with group members" },
        { type: "circle", id: "design" },
      ],
    });
    expect(notice?.tone).toBe("warning");
    expect(notice?.title).toBe("Published, but not shared with 3 recipients");
    expect(notice?.items).toEqual({
      summary: "Who was refused (3)",
      entries: [
        { id: "bob", kind: "person", reason: "Sharing is only allowed with group members" },
        { id: "marketing", kind: "group", reason: "Sharing is only allowed with group members" },
        { id: "design", kind: "Team", reason: "" },
      ],
    });
    expect(notice?.hint).toBe("Nextcloud refused them because of a sharing setting. Doctor › Recording sharing names it and the fix.");
    expect(notice?.action).toEqual({
      kind: "reshare",
      label: "Re-share with the room",
      note: "Adds whoever is missing, including anyone removed by hand. People who accept shares by hand will find it waiting in Browse.",
    });
    // Not while the meeting is being processed again.
    expect(shareNotice({ status: "partial", skipped: [{ type: "user", id: "bob" }] }, true, false)?.action).toBeUndefined();
  });

  it("counts against the room's audience when the operator had it at hand", () => {
    const skipped = [{ type: "user", id: "bob" }, { type: "user", id: "carol" }, { type: "group", id: "sales" }, { type: "user", id: "dave" }];
    expect(shareNotice({ status: "partial", skipped, audience_count: 8 })?.title).toBe("Published, but not shared with 4 of 8 recipients");
    expect(shareNotice({ status: "partial", skipped: skipped.slice(0, 1), audience_count: 8 })?.title).toBe("Published, but not shared with 1 of 8 recipients");
    expect(shareNotice({ status: "partial", skipped: skipped.slice(0, 1) })?.title).toBe("Published, but not shared with 1 recipient");
    // A count the operator could not vouch for is no count.
    expect(shareNotice({ status: "partial", skipped, audience_count: 0 })?.title).toBe("Published, but not shared with 4 recipients");
  });

  it("says who Nextcloud's share limit held back, apart from the refused, with the re-share", () => {
    const held = [{ type: "user", id: "dave" }, { type: "group", id: "marketing" }];
    const alone = shareNotice({ status: "deferred", deferred: held });
    expect(alone?.tone).toBe("warning");
    expect(alone?.title).toBe("Published, but Nextcloud's share limit held back 2 recipients. Re-share in 10 minutes, or raise the limit (Doctor › Recording sharing).");
    expect(alone?.items).toBeUndefined();
    expect(alone?.action?.label).toBe("Re-share with the room");
    expect(shareNotice({ status: "deferred", deferred: held.slice(0, 1) })?.title).toBe(
      "Published, but Nextcloud's share limit held back 1 recipient. Re-share in 10 minutes, or raise the limit (Doctor › Recording sharing).",
    );
    expect(shareNotice({ status: "deferred", deferred: held }, true, false)?.action).toBeUndefined();
    // With refusals too: the refused notice, the held-back sentence under it.
    const both = shareNotice({ status: "partial", skipped: [{ type: "user", id: "bob" }], deferred: held });
    expect(both?.title).toBe("Published, but not shared with 1 recipient");
    expect(both?.items?.summary).toBe("Who was refused (1)");
    expect(both?.heldBack).toBe("Nextcloud's share limit also held back 2.");
    expect(both?.action?.kind).toBe("reshare");
    expect(shareNotice({ status: "partial", skipped: [{ type: "user", id: "bob" }] })?.heldBack).toBeUndefined();
  });

  it("says a run was not published because its starter was refused, by name", () => {
    const notice = shareNotice({ status: "starter_refused", skipped: [{ type: "user", id: "mallory", reason: "Sharing is only allowed with group members" }] });
    expect(notice?.tone).toBe("error");
    expect(notice?.title).toBe("Not published: Nextcloud refused to share it with mallory, who started the recording.");
    expect(notice?.detail).toBe("mallory: Sharing is only allowed with group members");
    expect(notice?.hint).toBe("Fix the setting Doctor › Recording sharing names, then Rerun. Rerun publishes it and shares it with the whole room.");
    // A run that cannot be rerun is not promised one, and a starter the
    // operator did not record is not invented.
    const unnamed = shareNotice({ status: "starter_refused", skipped: [] }, false);
    expect(unnamed?.title).toBe("Not published: Nextcloud refused to share it with the person who started the recording.");
    expect(unnamed?.hint).toBe("Fix the setting Doctor › Recording sharing names so later recordings publish. This run cannot be rerun.");
    expect(unnamed?.detail).toBeUndefined();
  });
});

describe("after a re-share", () => {
  it("says everyone, or who is still refused, naming up to three", () => {
    expect(reshareOutcome([])).toBe("Shared with everyone.");
    expect(reshareOutcome(["bob"])).toBe("Shared again. Nextcloud still refused 1 recipient (bob). Check Doctor › Recording sharing.");
    expect(reshareOutcome(["bob", "carol"])).toBe("Shared again. Nextcloud still refused 2 recipients (bob and carol). Check Doctor › Recording sharing.");
    expect(reshareOutcome(["bob", "carol", "dave"])).toBe("Shared again. Nextcloud still refused 3 recipients (bob, carol and dave). Check Doctor › Recording sharing.");
    expect(reshareOutcome(["bob", "carol", "dave", "erin", "frank"])).toBe("Shared again. Nextcloud still refused 5 recipients (bob, carol, dave and 2 more). Check Doctor › Recording sharing.");
    expect(listRefusedIds([])).toBe("");
  });

  it("says who the limit on new shares held back for later", () => {
    expect(reshareOutcome([], 0)).toBe("Shared with everyone.");
    expect(reshareOutcome([], 12)).toBe("Shared again. Nextcloud's share limit still held back 12 recipients; re-share in 10 minutes.");
    expect(reshareOutcome([], 1)).toBe("Shared again. Nextcloud's share limit still held back 1 recipient; re-share in 10 minutes.");
    expect(reshareOutcome(["bob"], 3)).toBe(
      "Shared again. Nextcloud still refused 1 recipient (bob). Check Doctor › Recording sharing. Nextcloud's share limit still held back 3 recipients; re-share in 10 minutes.",
    );
  });

  it("writes a refused entry as one line", () => {
    expect(describeRefused({ id: "bob", kind: "person", reason: "" })).toBe("bob");
    expect(describeRefused({ id: "marketing", kind: "group", reason: "Sharing is only allowed with group members" })).toBe("marketing (group): Sharing is only allowed with group members");
    expect(describeRefused({ id: "design", kind: "Team", reason: "" })).toBe("design (Team)");
  });
});
