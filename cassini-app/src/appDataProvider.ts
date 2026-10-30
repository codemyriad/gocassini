import { requestRoomName } from "./roomNames";
import {
  StaticCatalogProvider,
  OperatorListProvider,
  SpeakerEditsError,
  resolvePublishedUrl,
  type InsightRecord,
  type MeetingCatalogEntry,
  type SpeakerEditsDoc,
  type SpeakerEditsErrorCode,
  type SpeakerEditsState,
} from "cassini-viewer/dataProvider";
import {
  AnnotationError,
  type AnnotationRequest,
  type AnnotationBatchRequest,
  type AnnotationBatchResult,
  type AnnotationResult,
  type MeetingAnnotations,
  type TagJob,
  type TagUpdate,
  type TagVocabulary,
  type VocabularyTag,
} from "cassini-viewer/annotations";
import {
  SummaryRequestError,
  type SpeakerSummaryChoice,
  type SummaryRequestState,
} from "cassini-viewer/meetingSummary";

import {
  listInsights as fetchInsightRuns,
  readInsight,
  renameInsight as postInsightName,
  retryInsight as postInsightRetry,
} from "./insights/client";
import { insightFilesUrl } from "./insights/filesUrl";
import {
  cancelImport as postImportCancel,
  removeImport as postImportRemove,
  clearImportStaging as postClearImportStaging,
  listImports as fetchImports,
  retryImport as postImportRetry,
  searchImportSharees as queryImportSharees,
  stageImportUpload as davStageImportUpload,
  startImports as postImports,
} from "./imports/client";
import { nextcloudRootPath } from "./operator/ncSetup";
import type { AgentConnection, ImportFileRequest, ImportJobRecord, ImportSharee, ImportSharingRequest } from "cassini-viewer/dataProvider";

// The in-Nextcloud shell's data provider (D-626).
//
// Everything the browse surface reads — the catalog, the `.opus` files — is
// already served out of the published archive, so this IS the static provider
// plus the one thing only a deployment with an operator behind it can do:
// assemble several meetings into a context bundle.
//
// That is why it lives here and not in the viewing layer. A standalone export
// has no operator, and the honest answer there is that the capability is
// absent (see StaticCatalogProvider) rather than a second, browser-side
// assembly of a published format that would look right and drift.
export class AppDataProvider extends StaticCatalogProvider {
  loadRoomName(roomId: string) { return requestRoomName(roomId); }
  saveRoomName(roomId: string, customName: string | null, expectRevision: number) { return requestRoomName(roomId, {customName, expectRevision}); }
  // The operator list discovers recordings that still need annotation import.
  // Keep static asset loading/search, and the list's older-operator fallback.
  private readonly listing = new OperatorListProvider();

  override loadCatalog() {
    return this.listing.loadCatalog();
  }

  // GET published/meetings-context?ids=…,… — the same document
  // `cassini meetings context <the same ids in the same order>` prints, byte
  // for byte, because the operator answers it from the one implementation the
  // CLI uses. Nothing here reformats or re-joins anything: the response body IS
  // the bundle.
  //
  // `published/` is already declared GET,HEAD at USER level in the ExApp
  // manifest, so this needs no new route and no new permission — which is what
  // makes Prepare cheap enough to be a button in the browse list.
  async loadContextBundle(entries: readonly MeetingCatalogEntry[]): Promise<string> {
    if (entries.length === 0) {
      throw new Error("Pick at least one meeting first.");
    }
    const url = new URL(resolvePublishedUrl("meetings-context"));
    // ONE comma-separated `ids`, in pick order, because the document prints its
    // meetings in the order the caller named them.
    //
    // Not a repeated `id`: AppAPI's proxy is PHP, which collapses repeated
    // query parameters to the last value unless they carry a `[]` suffix. That
    // was measured against a real Nextcloud — three ids sent, one id logged —
    // and it fails in the worst possible way, with a 200 and a bundle of one
    // meeting that reads like a correct answer to a question nobody asked. The
    // operator still accepts repeated `id` for callers that are not behind the
    // proxy; only this wire format changed.
    url.searchParams.set("ids", entries.map((entry) => entry.id).join(","));
    // Sent explicitly rather than relying on the default, so the bytes cannot
    // change under this caller if the endpoint's default ever does.
    url.searchParams.set("format", "markdown");

    // no-store for the same reason the catalog is fetched that way: AppAPI
    // caches proxied GETs for an hour, and a bundle re-prepared after a
    // re-publish must not be served the previous archive's answer.
    const response = await fetch(url.toString(), { cache: "no-store" });
    if (!response.ok) {
      throw new Error(await describeBundleFailure(response));
    }
    return response.text();
  }

  // The MCP endpoint (D-813) is a sibling of the published archive, like the
  // insight routes, so its address is derived from the archive's: one rule for
  // "where is the server" on a proxied deployment. App passwords are made in
  // Nextcloud's own security settings, at the instance's root.
  agentConnection(): AgentConnection {
    const endpoint = new URL("../mcp", resolvePublishedUrl("")).toString();
    const root = nextcloudRootPath();
    const appPasswordsUrl =
      root === null ? null : new URL(`${root}/index.php/settings/user/security`, window.location.href).toString();
    return { endpoint, appPasswordsUrl };
  }

  // GET insights — this caller's own runs, newest first (D-721/D-700).
  //
  // The same capability rule as loadContextBundle, and the stronger case for
  // it: an insight is a stored run, and a standalone export has nowhere for one
  // to have been stored. StaticCatalogProvider therefore implements neither
  // method, and the browse list reads that absence as "this build has no
  // insights" rather than rendering an empty shelf.
  //
  // It relays what the operator served and does not filter, sort or hide a
  // status: a queued or running insight belongs in the list too, which is the
  // whole point of a record that exists before its content does.
  async listInsights(): Promise<InsightRecord[]> {
    return fetchInsightRuns();
  }

  // GET insights/<id>, for the `document` half of it.
  //
  // Separate from listInsights because listing N insights must not carry N
  // documents. The bytes are relayed untouched — the operator wrote them, and a
  // second opinion about markdown here would be a second implementation of a
  // published format.
  async loadInsightDocument(id: string): Promise<string> {
    return (await readInsight(id)).document;
  }

  // The annotation routes (D-746).
  loadTagVocabulary(): Promise<TagVocabulary> {
    return requestAnnotations("tags");
  }

  loadMeetingAnnotations(entry: MeetingCatalogEntry): Promise<MeetingAnnotations> {
    return requestAnnotations(meetingPath(entry));
  }

  applyAnnotationOps(entry: MeetingCatalogEntry, request: AnnotationRequest): Promise<AnnotationResult> {
    return requestAnnotations(meetingPath(entry), request);
  }

  applyAnnotationBatch(request: AnnotationBatchRequest): Promise<AnnotationBatchResult> {
    return requestAnnotations("batch", request);
  }

  updateTag(tagId: string, update: TagUpdate): Promise<{ tag: VocabularyTag; job: TagJob | null }> {
    return requestAnnotations(tagPath(tagId), update);
  }

  async mergeTag(tagId: string, intoTagId: string): Promise<TagJob> {
    return (await requestAnnotations<{ job: TagJob }>(`${tagPath(tagId)}/merge`, { into: intoTagId })).job;
  }

  async deleteTag(tagId: string): Promise<TagJob> {
    return (await requestAnnotations<{ job: TagJob }>(`${tagPath(tagId)}/delete`, {})).job;
  }

  // POST annotations/meetings/<id>/delete (D-861). Anyone who can read a
  // meeting can delete it; the operator verifies that and the service account
  // moves the recording into its trashbin. The response carries nothing the
  // viewer needs beyond success itself.
  async deleteMeeting(entry: MeetingCatalogEntry): Promise<void> {
    await requestAnnotations(`${meetingPath(entry)}/delete`, {});
  }

  async loadTagJob(): Promise<TagJob | null> {
    return (await requestAnnotations<{ job: TagJob | null }>("tags/job")).job;
  }

  // The speakers route: people's edits to one meeting's speakers, beside its
  // marks and under the same caller check. A save answers the state it left
  // the meeting in, which is `applying` until the operator has republished it.
  loadSpeakerEdits(entry: MeetingCatalogEntry): Promise<SpeakerEditsState> {
    return requestAnnotations(speakersPath(entry), undefined, readSpeakerEditsError);
  }

  // summary is sent only when given: an operator that predates the choice
  // refuses a field it does not know, and keep is what its absence means.
  saveSpeakerEdits(
    entry: MeetingCatalogEntry,
    expectRevision: number,
    doc: SpeakerEditsDoc,
    summary?: SpeakerSummaryChoice,
  ): Promise<SpeakerEditsState> {
    return requestAnnotations(speakersPath(entry), summary ? { expectRevision, doc, summary } : { expectRevision, doc }, readSpeakerEditsError);
  }

  // The meeting's summary as its readers can ask for it (D-814): what writing
  // or replacing it would cost, and asking.
  loadSummaryRequestState(entry: MeetingCatalogEntry): Promise<SummaryRequestState> {
    return requestAnnotations(summaryPath(entry), undefined, readSummaryRequestError);
  }

  requestSummary(entry: MeetingCatalogEntry, replace: boolean): Promise<SummaryRequestState> {
    return requestAnnotations(summaryPath(entry), { replace }, readSummaryRequestError);
  }

  // POST insights/<id>/retry (D-749). The run record comes back as the
  // operator now holds it — `queued`, attempt incremented — and the viewer
  // puts it in the list in place of the failed one. A 409 arrives as the
  // operator's own sentence (running, or already answered), which is an
  // answer rather than a failure.
  async retryInsight(id: string): Promise<InsightRecord> {
    return postInsightRetry(id);
  }

  // POST insights/<id>/name (D-791). Answers the run with its new name; an
  // empty name clears it.
  async renameInsight(id: string, name: string): Promise<InsightRecord> {
    return postInsightName(id, name);
  }

  // The document in Nextcloud Files (D-792), selected by the file id the
  // delivery recorded, in a folder read off this page's own URL: the app is
  // proxied on Nextcloud's origin, so Files is beside it.
  insightFilesUrl(record: InsightRecord): string {
    return insightFilesUrl(window.location, record.documentPath, nextcloudRootPath(), record.documentFileId ?? "");
  }

  // Meeting import (D-828). Only this provider implements the group: staging
  // writes into the caller's own Nextcloud Files and the jobs run on the
  // operator, neither of which a standalone export has, so the Import tab is
  // absent everywhere else rather than present and failing.
  stageImportUpload(file: File, onProgress?: (sentBytes: number, totalBytes: number) => void, sharing?: ImportSharingRequest): Promise<string> {
    return davStageImportUpload(file, onProgress, sharing);
  }

  startImports(files: ImportFileRequest[]): Promise<ImportJobRecord[]> {
    return postImports(files);
  }

  listImports(): Promise<ImportJobRecord[]> {
    return fetchImports();
  }

  cancelImport(id: string): Promise<void> {
    return postImportCancel(id);
  }

  retryImport(id: string): Promise<void> {
    return postImportRetry(id);
  }

  removeImport(id: string): Promise<void> { return postImportRemove(id); }
  clearImportStaging(): Promise<void> { return postClearImportStaging(); }

  searchImportSharees(query: string): Promise<ImportSharee[]> {
    return queryImportSharees(query);
  }
}

const meetingPath = (entry: MeetingCatalogEntry) => `meetings/${encodeURIComponent(entry.id)}`;
const tagPath = (tagId: string) => `tags/${encodeURIComponent(tagId)}`;
const speakersPath = (entry: MeetingCatalogEntry) => `${meetingPath(entry)}/speakers`;
const summaryPath = (entry: MeetingCatalogEntry) => `${meetingPath(entry)}/summary`;

// A sibling of the published archive, as the insight routes are. A body makes
// it a POST.
async function requestAnnotations<T>(
  path: string,
  body?: object,
  readError: (response: Response) => Promise<Error> = readAnnotationError,
): Promise<T> {
  const url = new URL(`../annotations/${path}`, resolvePublishedUrl("")).toString();
  const init: RequestInit =
    body === undefined
      ? { headers: { Accept: "application/json" } }
      : {
          method: "POST",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify(body),
        };
  // no-store: AppAPI caches a proxied GET for an hour.
  const response = await fetch(url, { ...init, cache: "no-store" });
  if (!response.ok) {
    throw await readError(response);
  }
  return (await response.json()) as T;
}

async function readErrorBody(response: Response): Promise<Record<string, unknown>> {
  try {
    const payload: unknown = JSON.parse(await response.text());
    return typeof payload === "object" && payload !== null ? (payload as Record<string, unknown>) : {};
  } catch {
    // Not JSON: the status is all there is.
    return {};
  }
}

const SPEAKER_EDITS_CODES: readonly string[] = [
  "revision-conflict",
  "busy",
  "invalid",
  "diarization-unavailable",
  "unavailable",
  "rate-limited",
  "not-found",
] satisfies SpeakerEditsErrorCode[];

// The speakers route's refusals. A 404 is "not-found" whatever its body says:
// an unknown meeting, one this caller may not read, and an operator older than
// the route all answer it, and none of them has anything to offer here. A 429
// is "rate-limited" whatever its body says: a proxy in front of the operator
// answers it with no JSON, only Retry-After.
async function readSpeakerEditsError(response: Response): Promise<SpeakerEditsError> {
  const body = await readErrorBody(response);
  const served = typeof body.error === "string" && SPEAKER_EDITS_CODES.includes(body.error) ? (body.error as SpeakerEditsErrorCode) : "";
  const byStatus: SpeakerEditsErrorCode | "" = response.status === 404 ? "not-found" : response.status === 429 ? "rate-limited" : "";
  return new SpeakerEditsError(response.status, byStatus || served, {
    revision: typeof body.revision === "number" ? body.revision : undefined,
    detail: typeof body.message === "string" ? body.message : undefined,
    reason: typeof body.reason === "string" ? body.reason : undefined,
    retryAfterMs: readRetryAfterMs(body, response),
  });
}

// How long a rate-limited caller waits: the body's retryAfterMs, else a
// Retry-After header in seconds, else unknown.
function readRetryAfterMs(body: Record<string, unknown>, response: Response): number | undefined {
  if (typeof body.retryAfterMs === "number" && Number.isFinite(body.retryAfterMs) && body.retryAfterMs >= 0) {
    return body.retryAfterMs;
  }
  const seconds = Number(response.headers.get("Retry-After") ?? "");
  return response.status === 429 && response.headers.has("Retry-After") && Number.isFinite(seconds) && seconds >= 0
    ? seconds * 1000
    : undefined;
}

// The operator's refusals of a summary request: 409 with a code (busy,
// has-summary, unavailable with its reason), 403 for a replacement this reader
// may not ask for, 429 past the hourly limit, 404 for a meeting it does not
// know. The status decides where the body carries a sentence, not a code.
async function readSummaryRequestError(response: Response): Promise<SummaryRequestError> {
  const body = await readErrorBody(response);
  const byStatus =
    response.status === 403 ? "forbidden" : response.status === 404 ? "not-found" : response.status === 429 ? "rate-limited" : "";
  const code = byStatus || (typeof body.error === "string" ? body.error : "");
  const message = typeof body.reason === "string" ? body.reason : typeof body.error === "string" ? body.error : response.statusText;
  return new SummaryRequestError(response.status, code, message);
}

async function readAnnotationError(response: Response): Promise<AnnotationError> {
  const body = await readErrorBody(response);
  return new AnnotationError(
    response.status,
    typeof body.error === "string" ? body.error : "",
    typeof body.tagId === "string" ? body.tagId : undefined,
  );
}

// describeBundleFailure turns a refusal into something a reader can act on.
async function describeBundleFailure(response: Response): Promise<string> {
  switch (response.status) {
    case 400:
      // Only here is the served message worth repeating: the endpoint knows
      // what was wrong with the request — which id was malformed, or how many
      // meetings is too many — and nothing this side of it can say that. The
      // other statuses answer in fixed terms ("404 page not found") that would
      // be worse than the sentence below.
      return (
        (await readServedMessage(response)) || "Cassini could not read that request for a bundle."
      );
    case 404:
      // Whether a meeting exists and whether this caller may read it are
      // deliberately one answer, so that asking cannot be used to find out.
      //
      // A deployment that does not serve this endpoint at all — an operator
      // older than it, or one publishing to a local directory — falls through
      // to the published file server and 404s identically, so the sentence
      // names that too rather than sending an administrator hunting a
      // permissions problem that is not there.
      return "One of these meetings is not available to you, or this deployment cannot assemble bundles.";
    case 502:
      // Both of this status's causes — no verified caller identity, and a
      // failed scan — are the operator being unable to read Nextcloud as this
      // user, which is what the sentence says. Neither is anything the reader
      // can fix from here, so it does not pretend to be actionable.
      return "Cassini could not read these meetings from Nextcloud.";
    default:
      return `Could not prepare the bundle (HTTP ${response.status}).`;
  }
}

// readServedMessage reads the endpoint's own explanation, whether it arrives as
// a JSON error envelope (the operator API's shape) or as the plain-text line
// Go's http.Error writes. Anything longer or multi-line is a page, not a
// message, and is discarded rather than pasted into the panel.
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
