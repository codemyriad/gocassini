// The insight routes, from the app (D-700).
//
// One question asked of several meetings, and the run record that answers it
// over the minutes it takes. Four calls:
//
//	POST insights            create a run          201 {run}
//	GET  insights            the caller's runs     200 {"insights":[{run}, …]}
//	GET  insights/<id>       one run + document    200 {run, "document": "…"}
//	POST insights/<id>/retry retry a FAILED run    200 {run}   409 otherwise
//
// They live under `insights` and not under `published/`, which is the archive's
// prefix and is declared GET,HEAD: these are the app's first mutating USER
// routes, and a POST hidden inside a GET declaration would hide the change that
// matters.
//
// Everything a reader is told about a run lives HERE rather than in
// GenerateCard.svelte, for the reason setupHealth.ts gives: the copy is the
// part worth testing, and a wrong sentence about why an insight failed is a
// worse failure than a missing one. The card renders decisions it did not make.

import { resolvePublishedUrl } from "cassini-viewer/dataProvider";

import type { FeatureNotice } from "../operator/setupHealth";
import type { OperatorPanel } from "../surfaceRouting";

// The status vocabulary is fixed by internal/insight's package doc, and this is
// a reading of it rather than a second copy: a build that invented a fifth
// state, or renamed one, would disagree with the record the operator stores.
export const INSIGHT_STATUSES = ["queued", "running", "succeeded", "failed"] as const;
export type InsightStatus = (typeof INSIGHT_STATUSES)[number];

// An id is "ins_" plus sixteen lowercase hex characters — the scheme
// internal/insight fixes, stable across the attempts of one run. It is checked
// before it is put in a URL, because an id that is not one is a path segment
// this app would otherwise ask the operator to interpret.
export const INSIGHT_ID_PATTERN = /^ins_[0-9a-f]{16}$/;

export interface InsightRun {
  id: string;
  status: InsightStatus;
  createdBy: string;
  attemptNumber: number;
  workflowId: string;
  workflowVersion: string;
  workflowSha256: string;
  meetingIds: string[];
  roomIds: string[];
  question: string;
  // The endpoint and model the attempt that produced the bytes actually
  // resolved to — empty until a run has started, and different from the last
  // attempt's whenever a retry re-resolved them.
  provider: string;
  model: string;
  // Where the document landed in the requester's own Nextcloud files. Empty
  // until the run succeeds; a failed run wrote nothing.
  documentPath: string;
  error: string;
  createdAt: string;
  updatedAt: string;
}

export interface InsightDocument {
  run: InsightRun;
  // The insight itself, as markdown. Empty while the run is unfinished.
  document: string;
}

export interface CreateInsightRequest {
  meetingIds: readonly string[];
  // Empty means "whatever this deployment configured", which is the only thing
  // a non-admin can ask for: the template registry is ADMIN at the proxy.
  workflow?: string;
  question?: string;
}

// isTerminalStatus is the whole of the polling stop condition. Anything that is
// not succeeded or failed is still moving.
export function isTerminalStatus(status: InsightStatus): boolean {
  return status === "succeeded" || status === "failed";
}

// InsightRequestError carries the status alongside the sentence, because 409 —
// a retry that raced the run it was retrying — is not an error to show in red,
// it is an answer.
export class InsightRequestError extends Error {
  status: number | null;

  constructor(status: number | null, message: string) {
    super(message);
    this.name = "InsightRequestError";
    this.status = status;
  }
}

// resolveInsightsUrl locates the insight routes.
//
// They are a SIBLING of the published archive, so the address is derived from
// the archive's rather than re-derived from scratch: resolvePublishedUrl is
// already the one rule for "where is the server" — the captured AppAPI proxy
// base in the embedded build, the SPA's own base everywhere else — and a second
// copy of that rule is a second thing to get wrong on a proxied deployment.
export function resolveInsightsUrl(path = ""): string {
  const publishedRoot = resolvePublishedUrl("");
  const relative = path === "" ? "../insights" : `../insights/${path}`;
  return new URL(relative, publishedRoot).toString();
}

type InsightAction = "create" | "list" | "read" | "retry";

export async function createInsight(
  request: CreateInsightRequest,
  fetchImpl: typeof fetch = fetch,
): Promise<InsightRun> {
  const meetingIds = request.meetingIds.filter((id) => id.trim() !== "");
  if (meetingIds.length === 0) {
    // Refused here rather than at the operator: an empty selection is not a
    // question anyone asked, and a round trip to be told so is a round trip.
    throw new InsightRequestError(null, "Pick at least one meeting first.");
  }
  const body: Record<string, unknown> = { meetingIds };
  const workflow = request.workflow?.trim() ?? "";
  if (workflow !== "") {
    body.workflow = workflow;
  }
  const question = request.question?.trim() ?? "";
  if (question !== "") {
    body.question = question;
  }
  return readRun(
    await send("create", resolveInsightsUrl(), fetchImpl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }),
  );
}

export async function listInsights(fetchImpl: typeof fetch = fetch): Promise<InsightRun[]> {
  const payload = await send("list", resolveInsightsUrl(), fetchImpl);
  const rows = isRecord(payload) ? payload.insights : null;
  if (!Array.isArray(rows)) {
    throw new InsightRequestError(null, "Cassini could not read the list of insights.");
  }
  return rows.map((row) => readRun(row));
}

export async function readInsight(
  id: string,
  fetchImpl: typeof fetch = fetch,
): Promise<InsightDocument> {
  const payload = await send("read", resolveInsightsUrl(insightIDSegment(id)), fetchImpl);
  return {
    run: readRun(payload),
    document: isRecord(payload) && typeof payload.document === "string" ? payload.document : "",
  };
}

export async function retryInsight(
  id: string,
  fetchImpl: typeof fetch = fetch,
): Promise<InsightRun> {
  return readRun(
    await send("retry", resolveInsightsUrl(`${insightIDSegment(id)}/retry`), fetchImpl, {
      method: "POST",
    }),
  );
}

function insightIDSegment(id: string): string {
  if (!INSIGHT_ID_PATTERN.test(id)) {
    throw new InsightRequestError(null, `Not an insight id: ${id}`);
  }
  return id;
}

async function send(
  action: InsightAction,
  url: string,
  fetchImpl: typeof fetch,
  init?: RequestInit,
): Promise<unknown> {
  let response: Response;
  try {
    response = await fetchImpl(url, {
      ...init,
      headers: { Accept: "application/json", ...(init?.headers ?? {}) },
      // AppAPI caches a proxied GET for an hour, and the whole point of reading
      // a run is that its answer changes while you watch it. A cached status
      // would leave a finished insight showing "running" until the hour was up.
      cache: "no-store",
    });
  } catch (error) {
    throw new InsightRequestError(null, describeTransportFailure(action, error));
  }
  if (!response.ok) {
    throw new InsightRequestError(
      response.status,
      describeRequestFailure(action, response.status, await readServedMessage(response)),
    );
  }
  try {
    return await response.json();
  } catch {
    throw new InsightRequestError(response.status, "Cassini could not read the operator's answer.");
  }
}

// readRun insists on an id and a status this build understands, and is tolerant
// about everything else. The two it insists on are the two the card acts on: an
// unrecognised status would either stop the polling of a run that is still
// going or poll a finished one forever, and both are worse than saying so.
export function readRun(value: unknown): InsightRun {
  if (!isRecord(value)) {
    throw new InsightRequestError(null, "Cassini could not read the operator's answer.");
  }
  const id = asString(value.id);
  const status = value.status;
  if (id === "" || !INSIGHT_STATUSES.includes(status as InsightStatus)) {
    throw new InsightRequestError(
      null,
      `Cassini does not understand this insight record (status ${JSON.stringify(status)}).`,
    );
  }
  return {
    id,
    status: status as InsightStatus,
    createdBy: asString(value.createdBy),
    attemptNumber: typeof value.attemptNumber === "number" ? value.attemptNumber : 0,
    workflowId: asString(value.workflowId),
    workflowVersion: asString(value.workflowVersion),
    workflowSha256: asString(value.workflowSha256),
    meetingIds: asStrings(value.meetingIds),
    roomIds: asStrings(value.roomIds),
    question: asString(value.question),
    provider: asString(value.provider),
    model: asString(value.model),
    documentPath: asString(value.documentPath),
    error: asString(value.error),
    createdAt: asString(value.createdAt),
    updatedAt: asString(value.updatedAt),
  };
}

// --- What a run is told to say ---

// The poll schedule (D-720). A run over five meetings on a locally hosted model
// is minutes, so a one-second poll would ask the operator several hundred times
// for an answer that changes twice. It starts responsive, because queued ->
// running happens as soon as the operator picks the run up, and backs off to a
// quarter of a minute, which is the resolution a minutes-long run actually has.
// The caller resets the round whenever something changed, so a run that moves
// is asked about promptly again.
export const FIRST_POLL_DELAY_MS = 2_000;
export const MAX_POLL_DELAY_MS = 15_000;

export function pollDelayMs(round: number): number {
  const delay = FIRST_POLL_DELAY_MS * Math.pow(1.5, Math.max(0, round));
  return Math.min(Math.round(delay), MAX_POLL_DELAY_MS);
}

// describeRunProgress is what an unfinished run says while you watch it. The
// prototype showed 900ms of "Generating…"; the honest version names the wait,
// because a person who is not told a local model takes minutes reads a slow run
// as a broken one and presses Generate again.
export function describeRunProgress(run: InsightRun): string {
  const meetings = countMeetings(run.meetingIds.length);
  switch (run.status) {
    case "queued":
      return "Queued. Nothing has been sent to a model yet.";
    case "running":
      return (
        `Running. Cassini is reading ${meetings} and waiting for the model — on a model hosted ` +
        `on this deployment that is minutes, not seconds.`
      );
    case "succeeded":
      return `Ready, from ${meetings}.`;
    default:
      return "";
  }
}

// The four kinds of failure internal/insight classifies, because the answers
// differ. They are matched on the operator's own reason token rather than on a
// sentence: a sentence changes whenever someone improves it.
export type InsightFailureReason =
  | "no-provider"
  | "provider-refused"
  | "model-failed"
  | "bad-request"
  | "unknown";

export function classifyRunError(error: string): InsightFailureReason {
  const text = error.toLowerCase();
  for (const reason of ["no-provider", "provider-refused", "model-failed", "bad-request"] as const) {
    if (text.includes(reason)) {
      return reason;
    }
  }
  return "unknown";
}

// The one panel behind every AI failure: the endpoint, its key, its model and
// its request bounds are all edited in AI providers (Settings.svelte maps
// `endpoints` -> LLMSettingsPanel), which is also where buildFeatureNotice
// sends an administrator. Same panel, same words, deliberately — it is the same
// trip.
const AI_PANEL: OperatorPanel = "endpoints";
const ADMIN_ACTION = "Open AI providers";

const NOT_YOURS_TO_FIX =
  " Only a Nextcloud administrator can change this deployment's AI configuration, and there is " +
  "nothing wrong with your account.";

// buildRunFailureNotice turns a failed run into the NeedsSetupCard the app
// already renders for every other "this deployment cannot do that yet" state.
// A spinner that stops is not an error message, so every branch names the cause
// and — where an administrator could act on it — the panel that fixes it.
//
// A non-admin is never offered the link: that panel is ADMIN at the proxy and
// its PUT would 403, so offering the control would be offering a way to fail.
export function buildRunFailureNotice(options: {
  run: InsightRun;
  isAdmin: boolean;
}): FeatureNotice | null {
  const { run, isAdmin } = options;
  if (run.status !== "failed") {
    return null;
  }
  const reason = classifyRunError(run.error);
  const { title, summary, fixable } = FAILURE_COPY[reason];
  const reported = run.error.trim() === "" ? "" : ` The operator reported: ${run.error.trim()}`;
  const remediable = fixable && isAdmin;
  return {
    title,
    summary: summary + (fixable && !isAdmin ? NOT_YOURS_TO_FIX : "") + reported,
    panel: remediable ? AI_PANEL : "",
    actionLabel: remediable ? ADMIN_ACTION : "",
  };
}

// fixable means "a setting in AI providers is what changes the outcome", which
// is what decides whether an administrator is offered a link. A bad request is
// the one failure no endpoint configuration repairs.
const FAILURE_COPY: Record<
  InsightFailureReason,
  { title: string; summary: string; fixable: boolean }
> = {
  "no-provider": {
    title: "No AI endpoint is configured",
    summary:
      "This insight never reached a model, because this deployment has no AI endpoint it can " +
      "use. Retry re-resolves the endpoint and model from the settings as they stand at that " +
      "moment, so configuring one first is what makes a retry work.",
    fixable: true,
  },
  "provider-refused": {
    title: "The endpoint rejected the request",
    summary:
      "The AI endpoint answered and refused — usually a missing or rejected key, or a quota. " +
      "Retry re-resolves the endpoint, its key and its model from the settings as they stand at " +
      "that moment, so fixing the credential first is what makes a retry work.",
    fixable: true,
  },
  "model-failed": {
    title: "The model did not answer",
    summary:
      "The endpoint was reached but produced no usable answer — a timeout, an unreachable host, " +
      "or a server error. This is the failure a straight Retry is a sensible response to; if it " +
      "keeps timing out, the endpoint's request timeout is the setting that governs it.",
    fixable: true,
  },
  "bad-request": {
    title: "Cassini could not run that request",
    summary:
      "The run was refused before anything was sent to a model — an unknown template, or a " +
      "selection this deployment will not assemble. Changing the AI configuration will not " +
      "change the answer; changing the template or the meetings will.",
    fixable: false,
  },
  unknown: {
    title: "The insight failed",
    summary: "The run did not finish, and nothing was written to your files.",
    fixable: true,
  },
};

// --- Request failures, as opposed to run failures ---

function describeTransportFailure(action: InsightAction, error: unknown): string {
  const detail = error instanceof Error && error.message !== "" ? ` (${error.message})` : "";
  return `Could not reach Cassini to ${VERBS[action]}${detail}.`;
}

const VERBS: Record<InsightAction, string> = {
  create: "start the insight",
  list: "list your insights",
  read: "read the insight",
  retry: "retry the insight",
};

export function describeRequestFailure(
  action: InsightAction,
  status: number,
  served: string,
): string {
  switch (status) {
    case 400:
      // Only here is the served message worth repeating: the operator knows
      // what was wrong — which template is unknown, how many meetings is too
      // many — and nothing this side of it can say that.
      return served || "Cassini could not read that request for an insight.";
    case 404:
      // Whether it exists and whether this caller may read it are deliberately
      // one answer, so that asking cannot be used to find out. The sentence
      // also covers a deployment whose operator does not serve these routes at
      // all, which 404s identically.
      switch (action) {
        case "create":
          return "One of these meetings is not available to you, or this deployment cannot create insights.";
        case "list":
          // A caller's own list always exists, so the only thing a 404 can mean
          // here is an operator that does not serve these routes — an install
          // older than them, or one registered before they were declared. That
          // is not "you have no insights", and it must not read as it.
          return "This deployment cannot create insights yet.";
        default:
          return "That insight is not available to you, or this deployment cannot create insights.";
      }
    case 409:
      // Not a failure: the run is already moving, which is what the caller
      // wanted. The card re-reads it rather than painting an error.
      return "That insight is already running — retrying does nothing until it stops.";
    case 502:
      return "Cassini could not read these meetings from Nextcloud.";
    default:
      return `Could not ${VERBS[action]} (HTTP ${status}).`;
  }
}

// readServedMessage reads the operator's own explanation out of its JSON error
// envelope, or out of the plain-text line Go's http.Error writes. Anything
// longer or multi-line is a page, not a message, and is discarded rather than
// pasted into the panel.
async function readServedMessage(response: Response): Promise<string> {
  let body: string;
  try {
    body = (await response.text()).trim();
  } catch {
    return "";
  }
  if (body === "") {
    return "";
  }
  try {
    const payload = JSON.parse(body) as { error?: unknown };
    if (typeof payload.error === "string" && payload.error.trim() !== "") {
      return payload.error.trim();
    }
  } catch {
    // Not JSON; fall through to the plain-text form.
  }
  if (body.length <= 200 && !body.includes("\n") && !body.startsWith("<")) {
    return body;
  }
  return "";
}

function countMeetings(count: number): string {
  return count === 1 ? "one meeting" : `${count} meetings`;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asStrings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
