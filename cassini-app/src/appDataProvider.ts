import {
  StaticCatalogProvider,
  resolvePublishedUrl,
  type MeetingCatalogEntry,
} from "cassini-viewer/dataProvider";

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
  // GET published/meetings-context?id=…&id=… — the same document
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
    for (const entry of entries) {
      // Repeated `id` params, in pick order: the document prints its meetings
      // in the order the caller named them.
      url.searchParams.append("id", entry.id);
    }
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
