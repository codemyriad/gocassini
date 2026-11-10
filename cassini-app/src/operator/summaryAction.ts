// Writing or rewriting a run's summary from the Operator (D-814): what the
// button says, and what the confirmation says it will cost. Writing a summary
// calls a language model, which may be billed, so nothing is sent before the
// administrator has seen how many calls, to which model, of what size, and
// whether it leaves this network. Pure so the wording can be tested.
import { get } from "svelte/store";
import { LL, operatorPlace } from "cassini-viewer/i18n";
import { summaryCostLine, summaryEndpointLine } from "cassini-viewer/meetingSummary";

import type { SummaryPlan } from "./types";

export function summaryActionLabel(plan: SummaryPlan): string {
  const t = get(LL).common.summary_request;
  return plan.has_summary ? t.regenerate() : t.write();
}

export function summaryConfirmTitle(plan: SummaryPlan): string {
  const t = get(LL).common.summary_request;
  return plan.has_summary ? t.confirm_replace_title() : t.confirm_write_title();
}

// "1 call to deepseek/deepseek-v4.1-flash (Open Router) · about 7.5K input tokens, 5,213 words",
// worded as on the meeting itself.
export function summaryCostText(plan: SummaryPlan): string {
  return summaryCostLine({
    calls: plan.calls,
    words: plan.words,
    estimatedInputTokens: plan.estimated_input_tokens,
    provider: plan.provider,
    model: plan.model,
  });
}

export function summaryEndpointText(plan: SummaryPlan): string {
  return summaryEndpointLine(plan);
}

export function summaryReplaceText(plan: SummaryPlan): string | null {
  return plan.has_summary ? get(LL).app.insights.summary_action.replace_note() : null;
}

// Why the action is not offered, when an administrator can do something
// about it; null when there is nothing to say (no transcript, not published,
// busy: the run detail already says so).
export function summaryUnavailableText(plan: SummaryPlan): string | null {
  const t = get(LL).app.insights.summary_action;
  switch (plan.reason) {
    case "summaries_off":
      return t.summaries_off({ place: operatorPlace("publish_pipeline") });
    case "no_local_copy":
      return t.no_local_copy();
    default:
      return null;
  }
}

// Whether an LLM endpoint is on this host or network, by the operator's rule
// (localEndpoint in summarize_attempt.go): a loopback or private address, a
// .local/.localhost/.internal name, or a name with no domain (a container on
// the same Docker network). Anything else may be a billed service.
export function isLocalEndpoint(baseUrl: string): boolean {
  let host: string;
  try {
    host = new URL(baseUrl.trim()).hostname.toLowerCase().replace(/^\[|\]$/g, "");
  } catch {
    return false;
  }
  if (host === "") return false;
  if (host === "localhost" || /\.(localhost|local|internal)$/.test(host)) return true;
  const v4 = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])];
    return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254);
  }
  if (host.includes(":")) return host === "::1" || /^f[cd]/.test(host) || host.startsWith("fe80:");
  return !host.includes(".");
}

// What Settings says about the summary step's cost, for the chosen provider.
export function summaryStepLead(providerName: string, baseUrl: string): string {
  const t = get(LL).app.insights.summary_action;
  return isLocalEndpoint(baseUrl)
    ? t.step_lead_local({ provider: providerName })
    : t.step_lead_external({ provider: providerName });
}
