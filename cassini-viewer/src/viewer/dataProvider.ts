// DataProvider seam (D-415).
//
// App.svelte used to call the loadArtifact/catalog module functions directly
// and branch inline on `audioPath ?? artifactPath` to pick a loader. That hard-
// wired the viewer to the static-fetch data source. DataProvider is a thin,
// typed seam over the EXISTING loader functions and types so App.svelte depends
// on an interface instead of concrete module functions. This is a pure internal
// refactor: StaticCatalogProvider wraps the same loaders and reproduces the
// exact routing that used to live in App.svelte, so standalone behaviour is
// unchanged.

import {
  loadArtifactFromDirectory,
  loadBundledArtifact,
  loadPortableArtifactFromAudioPath,
  loadPortableMeetingSummary,
  switchPortableTranscript,
  PortableMeetingStore,
  type LoadedArtifact,
  type PortableMeetingSummary,
} from "./loadArtifact";
import {
  loadMeetingCatalog,
  loadMeetingsList,
  type MeetingCatalog,
  type MeetingCatalogEntry,
} from "./catalog";
import { readViewerBase, resolveAppBaseUrl } from "./appBase";
import type { InsightRecord } from "./insights";

// Re-exported so a provider implemented outside this package (cassini-app's,
// which has an operator behind it) can type its methods without reaching past
// the viewing layer's published entry points.
export type { MeetingCatalogEntry, InsightRecord };

// DataProvider mirrors ONLY what App.svelte actually calls. Every method is
// keyed off a MeetingCatalogEntry (or nothing) so the caller never has to know
// whether a meeting is a packed `.opus` (audioPath) or a loose artifact
// directory (artifactPath) — that routing lives in the implementation.
export interface DataProvider {
  // Runtime meeting catalog, or null when there is no catalog at all (a
  // standalone single-artifact export). An empty-but-present catalog is NOT
  // null — it means catalog/list mode with zero meetings.
  loadCatalog(): Promise<MeetingCatalog | null>;
  // Load a meeting's full artifact, routing on audioPath (packed `.opus`) vs
  // artifactPath (loose dev-only directory).
  loadMeetingForEntry(entry: MeetingCatalogEntry): Promise<LoadedArtifact>;
  // Lightweight speaker/segment/duration counts for catalog card hydration.
  // Only meaningful for audioPath (portable) entries; null otherwise.
  loadMeetingSummary(entry: MeetingCatalogEntry): Promise<PortableMeetingSummary | null>;
  // Switch the active transcript on an already-loaded portable meeting.
  switchTranscript(entry: MeetingCatalogEntry, transcriptId: string): Promise<LoadedArtifact>;
  // No-catalog standalone fallback: a single bundled artifact next to index.html.
  loadBundledArtifact(): Promise<LoadedArtifact>;

  // OPTIONAL (D-626): assemble the picked meetings into one
  // cassini.meetings.context.v1 document — the bytes `cassini meetings context`
  // prints for the same ids in the same order.
  //
  // Optional because it is a capability, not a fallback: a deployment either
  // has a producer that can assemble the bundle or it has none, and there is no
  // third answer where the viewer assembles a lookalike from what it happens to
  // have downloaded. Two implementations of one published format is exactly the
  // drift the version string exists to make visible, so the absence of this
  // method is the honest answer for a standalone export — App.svelte reads it
  // as "this build does not offer Prepare" and offers nothing.
  //
  // Meetings arrive in the order the caller picked them, because that is the
  // order the document prints in.
  loadContextBundle?(entries: readonly MeetingCatalogEntry[]): Promise<string>;

  // OPTIONAL (D-721): the insight runs this caller may read — `GET insights`,
  // newest first. Every run, whatever its status: a queued or running insight
  // belongs in the list too, or it materialises out of nowhere a minute later.
  //
  // Optional for the same reason loadContextBundle is, and it is the same
  // fact: an insight exists because an operator ran one. A standalone export
  // has no operator, so it has no insights — and the type filter, the cards and
  // the document all go with the absence of this method rather than rendering
  // an empty shelf that implies a feature this build does not have.
  //
  // The whole list is returned, not a page of it: this reads one caller's own
  // runs, and the list is the size of what one person asked for.
  listInsights?(): Promise<InsightRecord[]>;

  // OPTIONAL (D-721): one insight's document — the `document` field of
  // `GET insights/<id>`, which is markdown.
  //
  // Separate from listInsights because the wire is: listing N insights must not
  // carry N documents. A provider that lists insights but cannot fetch a
  // document is a coherent state — the cards still appear, and the sheet says
  // it cannot show the answer here rather than showing an empty page.
  loadInsightDocument?(id: string): Promise<string>;

  // OPTIONAL (D-749): retry a FAILED run — `POST insights/<id>/retry` — and
  // return the run as it now stands. The operator answers 409 for a run that
  // is not failed, which the implementation reports as its own sentence.
  //
  // Optional for the same reason the two above are: a retry is an operator
  // doing work, and a build with no operator offers no Retry control rather
  // than one that fails. The browse card and the document sheet both render it
  // when this exists, so a failed run is recoverable from wherever it is seen
  // rather than only from the panel that started it.
  retryInsight?(id: string): Promise<InsightRecord>;
}

// resolvePublishedUrl locates a file in the operator's published archive.
//
// It mirrors catalog.ts's own resolveCatalogUrl, which is the rule that already
// decides where `published/catalog.json` is fetched from: in the embedded build
// the archive is served by the operator under the captured AppAPI proxy base,
// and everywhere else it is resolved against the SPA's own base. Exported so a
// provider outside this package resolves it identically rather than
// re-deriving a second answer.
export function resolvePublishedUrl(path: string): string {
  const viewerBase = readViewerBase();
  if (viewerBase) {
    return new URL(`published/${path}`, viewerBase).toString();
  }
  return new URL(`published/${path}`, resolveAppBaseUrl()).toString();
}

// StaticCatalogProvider is the standalone/portable export's implementation: it
// serves meetings from the static-fetch loaders (catalog.json + published
// `.opus`/loose directories). It owns its own PortableMeetingStore so the
// portable manifest/body caches are scoped to this provider instead of being
// implicitly module-global.
//
// It deliberately does NOT implement loadContextBundle. There is no operator
// behind a static export — nothing that can produce the bundle — and the
// alternative, assembling one in the browser from the `.opus` files it can
// already read, would be a second implementation of a published format that
// looks right and drifts. Saying "I cannot" is the honest answer, and the
// Prepare affordance simply does not appear.
//
// It implements neither insight method for the same reason, and it is the
// stronger case: an insight is a stored run, and a static export has nowhere
// for one to have been stored. A build with no insights and a build whose
// insights failed to load are different states (D-721), and the way this one
// says which it is, is by not offering the capability at all — so the type
// filter is absent rather than reading "Insights 0".
export class StaticCatalogProvider implements DataProvider {
  private readonly portableStore = new PortableMeetingStore();

  loadCatalog(): Promise<MeetingCatalog | null> {
    return loadMeetingCatalog();
  }

  async loadMeetingForEntry(entry: MeetingCatalogEntry): Promise<LoadedArtifact> {
    // Primary published path: a `.opus` portable meeting loads via its
    // audioPath. Directory loading (artifactPath) is a dev-only affordance used
    // only when an entry has no audioPath — so audioPath is checked first even
    // if both happen to be present.
    if (entry.audioPath) {
      return loadPortableArtifactFromAudioPath(entry.audioPath, this.portableStore);
    }
    if (entry.artifactPath) {
      return loadArtifactFromDirectory(entry.artifactPath);
    }
    throw new Error(`Meeting ${entry.id} is missing artifactPath and audioPath`);
  }

  loadMeetingSummary(entry: MeetingCatalogEntry): Promise<PortableMeetingSummary | null> {
    if (!entry.audioPath) {
      return Promise.resolve(null);
    }
    return loadPortableMeetingSummary(entry.audioPath, this.portableStore);
  }

  switchTranscript(entry: MeetingCatalogEntry, transcriptId: string): Promise<LoadedArtifact> {
    if (!entry.audioPath) {
      return Promise.reject(
        new Error(`Meeting ${entry.id} has no audioPath; cannot switch transcript`),
      );
    }
    return switchPortableTranscript(entry.audioPath, transcriptId, this.portableStore);
  }

  loadBundledArtifact(): Promise<LoadedArtifact> {
    return loadBundledArtifact();
  }
}

// OperatorListProvider reads the meeting list from the operator's
// `published/meetings-list` endpoint (D-701) instead of from catalog.json.
//
// Only the LIST comes from there. Everything else — artifacts, summaries,
// transcript switching — is an asset fetch that has not changed, so it is
// delegated to a wrapped StaticCatalogProvider rather than reimplemented. That
// also keeps a single PortableMeetingStore behind both, so the portable
// manifest cache is not split in half.
//
// Why the viewer wants the endpoint at all, given it fetches the whole list
// either way and filters client-side: catalog.json cannot distinguish a
// substrate failure from an empty archive. It answers 200 with an empty catalog
// when the per-caller scan fails or the recordings mount is missing, and
// App.svelte then replaces the list with nothing — the archive silently blanks
// during a Nextcloud hiccup, and recovers only on the next poll that happens to
// succeed. The endpoint reports those as an error status, which throws, and the
// refresh path already knows how to keep the last known-good list and surface
// the reason.
//
// Server-side narrowing (from/to/room) is deliberately NOT used yet. The list
// view filters and groups client-side over the full set, and moving that to the
// server is a separate decision about a much larger archive than any deployment
// currently has.
export class OperatorListProvider implements DataProvider {
  private readonly assets = new StaticCatalogProvider();
  // Latched once the endpoint answers 404, so a deployment that does not serve
  // it does not pay a failed request on every refresh poll. The cost is that an
  // operator upgraded while this tab stays open keeps using catalog.json until
  // a reload — strictly no worse than the behaviour before this class existed.
  private endpointAbsent = false;

  async loadCatalog(): Promise<MeetingCatalog | null> {
    if (!this.endpointAbsent) {
      const result = await loadMeetingsList();
      if (result.status === "ok") {
        return result.catalog;
      }
      this.endpointAbsent = true;
    }
    // catalog.json remains the fallback for an operator older than the
    // endpoint, and for one publishing to the local sink, which does not serve
    // it. A failure HERE still throws, exactly as it did before.
    return this.assets.loadCatalog();
  }

  loadMeetingForEntry(entry: MeetingCatalogEntry): Promise<LoadedArtifact> {
    return this.assets.loadMeetingForEntry(entry);
  }

  loadMeetingSummary(entry: MeetingCatalogEntry): Promise<PortableMeetingSummary | null> {
    return this.assets.loadMeetingSummary(entry);
  }

  switchTranscript(entry: MeetingCatalogEntry, transcriptId: string): Promise<LoadedArtifact> {
    return this.assets.switchTranscript(entry, transcriptId);
  }

  loadBundledArtifact(): Promise<LoadedArtifact> {
    return this.assets.loadBundledArtifact();
  }
}
