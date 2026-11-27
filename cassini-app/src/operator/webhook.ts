// The Webhooks panel's rules (WebhookPanel.svelte). The operator validates
// every save (event_webhook.go); its coded refusals land on the field they
// are about through fieldForErrorCode.
import type { WebhookDelivery, WebhookSettings, WebhookSettingsUpdate } from "./types";

export const DEFAULT_TIMEOUT_SECONDS = 10;
export const SUMMARY_COMPLETED = "summary.completed";
export const MAX_SUMMARY_NOTICE_LENGTH = 200;

export type WebhookField = "url" | "timeout" | "secret" | "events" | "summary_notice";

// Suffixes of the app.settings.webhook.error_* keys.
export type WebhookFieldError =
  | "url"
  | "timeout"
  | "secret"
  | "secret_new_url"
  | "secret_short"
  | "events"
  | "event_unknown"
  | "summary_notice_invalid"
  | "summary_notice_mention"
  | "summary_notice_too_long";

export type WebhookErrors = Partial<Record<WebhookField, WebhookFieldError>>;

export interface WebhookDraft {
  url: string;
  // Bound to a number input: null when it is empty.
  timeoutSec: number | null;
  // Only what was typed or generated: an empty field on an existing receiver
  // keeps the stored secret.
  secret: string;
  events: string[];
  // Why summaries are sent; kept while summary.completed is unticked.
  summaryNotice: string;
}

// A new receiver starts with nothing ticked.
export function draftFrom(settings: WebhookSettings | null, existing: boolean): WebhookDraft {
  if (!settings || !existing) {
    return { url: "", timeoutSec: DEFAULT_TIMEOUT_SECONDS, secret: "", events: [], summaryNotice: "" };
  }
  return {
    url: settings.url,
    timeoutSec: settings.timeout_seconds,
    secret: "",
    events: [...settings.events],
    summaryNotice: settings.summary_notice,
  };
}

// The PUT for a draft: only what changed against the saved settings (all of
// it for a first receiver), and the secret only when one was typed.
export function webhookUpdate(draft: WebhookDraft, saved: WebhookSettings | null): WebhookSettingsUpdate {
  const fresh = !saved || saved.url === "";
  const update: WebhookSettingsUpdate = {};
  const url = draft.url.trim();
  if (fresh || url !== saved.url) {
    update.url = url;
  }
  const seconds = draft.timeoutSec ?? DEFAULT_TIMEOUT_SECONDS;
  if (fresh || seconds !== saved.timeout_seconds) {
    update.timeout_seconds = seconds;
  }
  if (fresh || draft.events.length !== saved.events.length || draft.events.some((name) => !saved.events.includes(name))) {
    update.events = [...draft.events];
  }
  const secret = draft.secret.trim();
  if (secret !== "") {
    update.secret = secret;
  }
  // Only while summary.completed is ticked, when the field is shown.
  const notice = normalizeSummaryNotice(draft.summaryNotice);
  if (draft.events.includes(SUMMARY_COMPLETED) && notice !== (fresh ? "" : saved.summary_notice)) {
    update.summary_notice = notice;
  }
  return update;
}

// The reason as the operator stores it: trimmed, spaces collapsed.
export function normalizeSummaryNotice(reason: string): string {
  return reason.trim().split(/\s+/).filter(Boolean).join(" ");
}

// The host a meeting will name the receiver at url by, or "" when the
// operator would refuse it: only a host the browser writes exactly as typed,
// lowercased (eventNoticeHost in notice_host.go). Never the port, path,
// query or credentials.
export function noticeHost(url: string): string {
  const typed = /^https?:\/\/(?:[^@/?#]*@)?(\[[^\]/?#]*\]|[^:/?#]*)/i.exec(url.trim())?.[1]?.toLowerCase();
  let host: string;
  try {
    host = new URL(url.trim()).hostname;
  } catch {
    return "";
  }
  if (!typed || host !== typed) {
    return "";
  }
  const bare = host.replace(/^\[(.*)\]$/, "$1");
  const plain =
    /^([a-z0-9_-]+\.)*[a-z0-9_-]+\.?$/.test(bare) || // a name (the browser refuses one ending in a number)
    /^(\d{1,3}\.){3}\d{1,3}$/.test(bare) || // dotted decimal, as the browser writes it
    (host.startsWith("[") && !bare.includes(".") && !bare.startsWith("::ffff:")); // IPv6, not IPv4-mapped
  return plain ? bare : "";
}

// The field a coded 400 belongs to (codes listed in docs/events.md), or null
// for a code this build does not know.
export function fieldForErrorCode(code: string): { field: WebhookField; error: WebhookFieldError } | null {
  switch (code) {
    case "webhook_url_invalid":
      return { field: "url", error: "url" };
    case "webhook_secret_required":
      return { field: "secret", error: "secret" };
    case "webhook_secret_required_for_new_url":
      return { field: "secret", error: "secret_new_url" };
    case "webhook_secret_too_short":
      return { field: "secret", error: "secret_short" };
    case "webhook_events_required":
      return { field: "events", error: "events" };
    case "webhook_event_unknown":
      return { field: "events", error: "event_unknown" };
    case "webhook_timeout_invalid":
      return { field: "timeout", error: "timeout" };
    case "webhook_summary_notice_invalid":
    case "webhook_summary_notice_mention":
    case "webhook_summary_notice_too_long":
      return { field: "summary_notice", error: code.slice("webhook_".length) as WebhookFieldError };
    default:
      return null;
  }
}

// Why a test send was refused, as the panel words it: no receiver saved, or a
// proxy in front of Cassini gave up waiting (502/504). Null: shown as it came.
export function testRefusal(status: number, code: unknown): "not_configured" | "gateway_timeout" | null {
  if (status === 409 && code === "webhook_not_configured") {
    return "not_configured";
  }
  return status === 502 || status === 504 ? "gateway_timeout" : null;
}

export type DeliveryStatus = "idle" | "delivering" | "failing";

// failing: events wait on an error newer than the last delivery (or nothing
// was ever delivered); given-up events alone do not count. idle: nothing in
// the last 7 days. delivering: everything else.
export function deliveryStatus(delivery: WebhookDelivery): DeliveryStatus {
  if (delivery.waiting > 0 && delivery.last_error !== "" && errorAfterDelivery(delivery)) {
    return "failing";
  }
  if (delivery.delivered === 0 && delivery.waiting === 0 && delivery.failed === 0 && delivery.last_error === "") {
    return "idle";
  }
  return "delivering";
}

// An untimed error counts as newer only when nothing was ever delivered. The
// store's fixed-width UTC times compare as strings.
function errorAfterDelivery(delivery: WebhookDelivery): boolean {
  if (delivery.last_delivered_at === "") {
    return true;
  }
  return delivery.last_error_at !== "" && delivery.last_error_at > delivery.last_delivered_at;
}

// A 401 or 403: almost always a receiver checking with a different secret.
export function isAuthRefusal(text: string): boolean {
  return /\bHTTP 40[13]\b/.test(text);
}

// 32 random bytes as hex, made in this browser and shown once.
export function generateSecret(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
