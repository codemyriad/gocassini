import { describe, expect, it } from "vitest";
import type { WebhookDelivery, WebhookSettings } from "./operator/types";
import hostFixture from "../../spec/testdata/webhook-notice-hosts.json";
import { deliveryStatus, draftFrom, fieldForErrorCode, noticeHost, webhookUpdate } from "./operator/webhook";

const quiet = (): WebhookDelivery => ({ last_delivered_at: "", delivered: 0, waiting: 0, failed: 0, last_error: "", last_error_at: "" });

const saved = (): WebhookSettings => ({
  url: "https://flows.example.org/hook",
  secret_set: true,
  events: ["recording.failed", "summary.completed"],
  timeout_seconds: 10,
  catalog: [],
  summary_notice: "",
  delivery: quiet(),
  error: "",
});

describe("webhookUpdate", () => {
  it("sends everything for a first receiver", () => {
    const draft = { url: " https://x.example/h ", timeoutSec: 15, secret: " typed ", events: ["summary.failed"], summaryNotice: "" };
    expect(webhookUpdate(draft, null)).toEqual({ url: "https://x.example/h", timeout_seconds: 15, events: ["summary.failed"], secret: "typed" });
  });

  it("sends only what changed, and the secret only when one was typed", () => {
    const current = saved();
    const unchanged = draftFrom(current, true);
    expect(unchanged.secret).toBe("");
    expect(webhookUpdate({ ...unchanged, events: [...unchanged.events].reverse() }, current)).toEqual({});
    expect(webhookUpdate({ ...unchanged, timeoutSec: 30 }, current)).toEqual({ timeout_seconds: 30 });
    expect(webhookUpdate({ ...unchanged, events: ["summary.failed"] }, current)).toEqual({ events: ["summary.failed"] });
    expect(webhookUpdate({ ...unchanged, url: "https://flows.example.org/other" }, current)).toEqual({ url: "https://flows.example.org/other" });
    expect(webhookUpdate({ ...unchanged, secret: "new" }, current)).toEqual({ secret: "new" });
  });
});

describe("deliveryStatus", () => {
  const failing = {
    ...quiet(),
    delivered: 21,
    waiting: 5,
    failed: 2,
    last_delivered_at: "2026-11-16T16:42:00.000000000Z",
    last_error: "receiver returned HTTP 401",
    last_error_at: "2026-11-17T09:12:00.000000000Z",
  };

  it.each([
    ["nothing sent", quiet(), "idle"],
    ["delivered", { ...quiet(), delivered: 38, last_delivered_at: "2026-11-17T09:08:11Z" }, "delivering"],
    ["queued, no error", { ...quiet(), delivered: 3, waiting: 2 }, "delivering"],
    ["waiting on a newer error", failing, "failing"],
    ["never delivered, untimed error", { ...failing, delivered: 0, last_delivered_at: "", last_error_at: "" }, "failing"],
    ["delivered since the error", { ...failing, last_delivered_at: "2026-11-17T10:00:00.000000000Z" }, "delivering"],
    ["untimed error after a delivery", { ...failing, last_error_at: "" }, "delivering"],
    ["nanoseconds apart, error newer", { ...failing, last_delivered_at: "2026-11-17T09:12:00.000000001Z", last_error_at: "2026-11-17T09:12:00.000000002Z" }, "failing"],
    ["given up only", { ...quiet(), failed: 2, last_error: "HTTP 500; gave up", last_error_at: "2026-11-17T09:12:00Z" }, "delivering"],
  ])("%s", (_, delivery, want) => {
    expect(deliveryStatus(delivery)).toBe(want);
  });
});

describe("summary notice (#532)", () => {
  // Shared with the operator's tests: the preview names the receiver as the
  // meeting will, and shows nothing for an address the operator refuses.
  it("names the receiver as the operator does", () => {
    for (const { url, host } of hostFixture.cases) {
      expect(noticeHost(url), url).toBe(host);
    }
  });

  it("sends the reason, trimmed, only while summary.completed is ticked", () => {
    const current = { ...saved(), summary_notice: "Kept in the wiki." };
    const draft = draftFrom(current, true);
    expect(webhookUpdate(draft, current)).toEqual({});
    expect(webhookUpdate({ ...draft, summaryNotice: "  Kept  in the team wiki. " }, current)).toEqual({ summary_notice: "Kept in the team wiki." });
    expect(webhookUpdate({ ...draft, events: ["summary.failed"], summaryNotice: "" }, current)).toEqual({ events: ["summary.failed"] });
  });

  it("puts the operator's refusals of the reason under its field", () => {
    for (const code of ["webhook_summary_notice_invalid", "webhook_summary_notice_mention", "webhook_summary_notice_too_long"]) {
      expect(fieldForErrorCode(code)).toEqual({ field: "summary_notice", error: code.slice("webhook_".length) });
    }
  });
});
