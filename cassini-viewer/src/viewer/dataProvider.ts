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
}

// StaticCatalogProvider is the only implementation: it serves meetings from the
// static-fetch loaders (catalog.json + published `.opus`/loose directories).
// It owns its own PortableMeetingStore so the portable manifest/body caches are
// scoped to this provider instead of being implicitly module-global.
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
