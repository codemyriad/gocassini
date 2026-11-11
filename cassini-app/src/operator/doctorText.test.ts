import { describe, expect, it } from "vitest";
import catalog from "./doctorCatalog.json";
import { blockedReason, blockedReasonWordings, checkMessage, messageWordings, readParams, stepLabel, stepWordings } from "./doctorText";
import type { ReadinessCheck } from "./readiness";

// doctorCatalog.json is generated from the operator's and the recorder's
// source (cassini-operator doctor_catalog_test.go). A code there with no
// wording here would show in English in every language, so it fails here.
describe("the catalog", () => {
  it("words every message code the operator can send", () => {
    expect(Object.keys(messageWordings).sort()).toEqual([...catalog.messages].sort());
  });
  it("words every remedy step the operator can send", () => {
    expect(Object.keys(stepWordings).sort()).toEqual([...catalog.steps].sort());
  });
  it("words every reason a recording can be blocked", () => {
    expect(Object.keys(blockedReasonWordings).sort()).toEqual([...catalog.blocked_reasons].sort());
  });
});

const row = (check: Partial<ReadinessCheck> & { code: string }): ReadinessCheck =>
  ({ id: "x", state: "needs_action", message: "OPERATOR ENGLISH", ...check });

// The English here is the operator's, copied from the Go source: the
// dictionary must say what the operator says.
describe("checkMessage", () => {
  it("words a fixed message from its code alone", () => {
    expect(checkMessage(row({ code: "hpb_missing" })))
      .toBe("Talk has no standalone signaling server configured. Enable its high-performance backend.");
  });

  it("picks the sentence by variant, row and state", () => {
    expect(checkMessage(row({ code: "test_in_progress", params: { variant: "in_progress_at_stage", stage: "build" } })))
      .toBe("A test recording is in progress: build.");
    expect(checkMessage(row({ code: "hpb_declared_unverified", params: { variant: "secret_from_env" } })))
      .toBe("Talk names a High Performance Backend, and the internal secret comes from Cassini's deployment configuration. Whether that secret is the right one is what this check establishes.");
    expect(checkMessage(row({ id: "talk.discovery", code: "recording_secret_missing" })))
      .toBe("Configure Cassini's recording credential and connect Talk first.");
    expect(checkMessage(row({ id: "talk.hpb", code: "internal_secret_missing", state: "not_verified" })))
      .toBe("Talk has standalone signaling configured. Supply its internal client secret to verify HPB authentication.");
    expect(checkMessage(row({ code: "talk_backend_url_invalid", params: { variant: "not_a_base_url" } })))
      .toBe("Cassini's configured Talk backend URL is not a usable base URL. It must be a plain http(s) URL with no query or fragment. Clear CASSINI_TALK_BACKEND_URL unless a deployment genuinely needs an override.");
  });

  it("names a blocking row by the panel's own label", () => {
    expect(checkMessage(row({ code: "check_blocked", params: { variant: "unchecked", blocker: "talk.hpb" } })))
      .toBe("Not checked: this depends on High Performance Backend, which has not been checked yet. Run all checks first.");
    expect(checkMessage(row({ code: "check_blocked", params: { variant: "needs_attention", blocker: "storage" } })))
      .toBe("Not checked: this depends on Recording storage, which needs attention first.");
  });

  it("words every sharing issue, in the operator's order", () => {
    expect(checkMessage(row({
      code: "sharing_disabled_for_account",
      params: { issues: ["excluded_groups", "group_sharing_off"], account: "cassini", excluded_groups: ["robots", "bots"] },
    }))).toBe("Nobody can receive recordings. Nextcloud's “Exclude some groups from sharing” lists “robots”, “bots”, and Cassini shares from its cassini account, which is in those groups. Every share is refused, so no new meetings can be published. Groups in a room do not receive recordings. Nextcloud's “Allow sharing with groups” is off, so a meeting whose room includes a group is published without access for that group's members, unless they are in the room themselves.");
    expect(checkMessage(row({
      code: "sharing_limited_to_groups", state: "warn",
      params: { issues: ["only_admins"], account: "cassini", shareable_groups: ["admin"] },
    }))).toBe("Only administrators can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its cassini account, which is only in “admin”, the administrators' group. Anyone who is not an administrator cannot open the recordings of their meetings, and a recording they start is not published at all.");
  });

  it("words search coverage and the last re-index", () => {
    const counts = { indexed: 3, silent: 1, disabled: 0, model_unavailable: 0, transcription_failed: 2, unknown_empty: 0,
      missing_transcript: 0, unreadable_transcript: 0, backfill_candidates: 0, other_unavailable: 0, untracked: 4 };
    expect(checkMessage(row({ code: "search_coverage_partial", state: "warn", params: { variant: "coverage_unlisted", ...counts } })))
      .toBe("3 meetings are searchable. Of the others, 1 was silent, 2 failed to transcribe and 4 are in the archive but not indexed yet. Cassini has not listed the archive yet, so older recordings may not be counted.");
    expect(checkMessage(row({ code: "search_archive_files_accounted_for", state: "passed",
      params: { variant: "coverage_complete", ...counts, silent: 0, transcription_failed: 0, untracked: 0, indexed: 1,
        reindex: "finished", reindex_added: 1, reindex_unchanged: 0, reindex_unsearchable: 0, reindex_failed: 0 } })))
      .toBe("1 meeting is searchable. Every meeting file in the archive is accounted for. The last re-index added 1, left 0 unchanged, found 0 not searchable and failed on 0.");
    expect(checkMessage(row({ code: "search_reindex_environment", params: { variant: "coverage", ...counts, indexed: 0, silent: 0, transcription_failed: 0, untracked: 0, reindex: "environment" } })))
      .toBe("No meetings are searchable yet. The last re-index could not read the settings AppAPI gives Cassini, so trying again would fail the same way.");
  });

  it("words the host rows by what doctor found", () => {
    expect(checkMessage(row({ id: "host.workdir", code: "workdir", state: "passed", params: { variant: "found", path: "/data" } })))
      .toBe("working directory /data");
    expect(checkMessage(row({ id: "host.tmpdir.writable", code: "tmpdir.writable", params: { variant: "not_writable", path: "/tmp", error: "permission denied" } })))
      .toBe("temporary directory is not writable: /tmp (permission denied)");
  });

  it("falls back to the operator's English for what it cannot word", () => {
    // A code from a newer operator.
    expect(checkMessage(row({ code: "something_new" }))).toBe("OPERATOR ENGLISH");
    // An older operator: no params.
    expect(checkMessage(row({ code: "test_failed" }))).toBe("OPERATOR ENGLISH");
    expect(checkMessage(row({ code: "sharing_no_common_group" }))).toBe("OPERATOR ENGLISH");
    // A variant this build does not know, or a param missing.
    expect(checkMessage(row({ code: "test_failed", params: { variant: "exploded" } }))).toBe("OPERATOR ENGLISH");
    expect(checkMessage(row({ code: "test_failed", params: { variant: "stopped_at_stage" } }))).toBe("OPERATOR ENGLISH");
    expect(checkMessage(row({ code: "check_blocked", params: { variant: "unchecked", blocker: "nowhere" } }))).toBe("OPERATOR ENGLISH");
    // Not a prototype property either.
    expect(checkMessage(row({ code: "toString" }))).toBe("OPERATOR ENGLISH");
  });
});

describe("stepLabel", () => {
  it("words a step by its id, with code spans kept", () => {
    expect(stepLabel({ id: "nextcloud_address", label: "x" }))
      .toBe("Cassini reaches Nextcloud at `NEXTCLOUD_URL`, or at `CASSINI_TALK_BACKEND_URL` when that is set. The address has to work from inside Cassini's container, not only from your browser");
    expect(stepLabel({ id: "leave_excluded_groups", label: "x", params: { account: "cassini", groups: ["robots", "bots"] } }))
      .toBe("Or take `cassini` out of `robots`, `bots` on Nextcloud's Users page.");
    expect(stepLabel({ id: "reindex_adds", label: "x", params: { count: 1 } }))
      .toBe("Re-index now adds the 1 recording that is not in search yet");
    expect(stepLabel({ id: "reindex_adds", label: "x", params: { count: 7 } }))
      .toBe("Re-index now adds the 7 recordings that are not in search yet");
  });

  it("falls back to the operator's English", () => {
    expect(stepLabel({ label: "doctor's own advice" })).toBe("doctor's own advice");
    expect(stepLabel({ id: "brand_new", label: "English" })).toBe("English");
    expect(stepLabel({ id: "unexclude_groups", label: "English" })).toBe("English");
  });
});

describe("blockedReason", () => {
  it("words a reason by its code and params", () => {
    expect(blockedReason({ meeting: "M.opus", reason: "x", reason_code: "file_id_changed", reason_params: { from: 41, to: 42 } }))
      .toBe("Nextcloud changed the file id from 41 to 42; existing shares must be repaired");
    expect(blockedReason({ meeting: "M.opus", reason: "x", reason_code: "cli_too_old" }))
      .toBe("the cassini CLI cannot set recordedAtUtc; upgrade it and retry");
  });

  it("keeps the English of a file an older operator blocked", () => {
    expect(blockedReason({ meeting: "M.opus", reason: "recording is missing; restore it and retry" }))
      .toBe("recording is missing; restore it and retry");
  });
});

describe("readParams", () => {
  it("keeps strings, numbers and string lists, and drops the rest", () => {
    expect(readParams({ a: "x", b: 2, c: ["y"], d: null, e: { f: 1 }, g: [1] })).toEqual({ a: "x", b: 2, c: ["y"] });
    expect(readParams(null)).toBeUndefined();
    expect(readParams({})).toBeUndefined();
  });
});
