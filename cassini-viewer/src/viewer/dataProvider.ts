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
  type MeetingCatalog,
  type MeetingCatalogEntry,
} from "./catalog";
import { readViewerBase, resolveAppBaseUrl } from "./appBase";

// Re-exported so a provider implemented outside this package (cassini-app's,
// which has an operator behind it) can type its methods without reaching past
// the viewing layer's published entry points.
export type { MeetingCatalogEntry };

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
