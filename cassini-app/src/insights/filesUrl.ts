// Where an insight's document opens in Nextcloud Files (D-792).
//
// The document is in the requester's own Files, so sharing it is Nextcloud's
// job: this only has to land them on the file, where the share sidebar is. The
// app is served through AppAPI on the same origin as Nextcloud, so the root is
// read off the page's own URL rather than configured — and it has to keep any
// subpath, because Nextcloud is not always installed at "/".

// nextcloudRoot is the origin plus Nextcloud's web root. Nextcloud's own page
// scripts say what that root is (`OC.getRootPath()`), and when they are on the
// page their answer wins: a guess from the path is wrong for an install whose
// subpath itself contains `/apps/`. Without them, it is everything before the
// first `/index.php/`, `/apps/` or `/exapps/`, whichever comes first: the
// embedded page is under `/index.php/apps/...`, a pretty-URL install serves
// `/apps/...` directly, and HaRP serves an ExApp's own pages under
// `/exapps/<appid>/`.
export function nextcloudRoot(
  location: Pick<Location, "origin" | "pathname">,
  knownRootPath: string | null = null,
): string {
  const { origin, pathname } = location;
  if (typeof knownRootPath === "string") {
    return origin + knownRootPath.replace(/\/+$/, "");
  }
  const cuts = ["/index.php/", "/apps/", "/exapps/"]
    .map((marker) => pathname.indexOf(marker))
    .filter((index) => index >= 0);
  if (cuts.length === 0) {
    return origin;
  }
  return origin + pathname.slice(0, Math.min(...cuts));
}

// insightFilesUrl opens the document in Nextcloud Files. The Files app since
// Nextcloud 28 (every version in info.xml's range) selects a file only by its
// numeric id in the route, `/apps/files/files/<id>`, and `opendetails` then
// opens the sidebar on it, where sharing is; the legacy `scrollto=` query is
// read by nothing it ships. With the id the document is selected and its
// details open; without one (a run delivered before the id was kept) the
// folder opens, and the file is in it.
//
// documentPath is relative to the requester's home, as the operator records it
// ("Cassini Insights/<file>.md").
export function insightFilesUrl(
  location: Pick<Location, "origin" | "pathname">,
  documentPath: string,
  knownRootPath: string | null = null,
  documentFileId = "",
): string {
  const path = documentPath.replace(/^\/+/, "");
  if (path === "") {
    return "";
  }
  const slash = path.lastIndexOf("/");
  const dir = slash >= 0 ? path.slice(0, slash) : "";
  const query = `?dir=${encodeURIComponent(`/${dir}`)}`;
  const files = `${nextcloudRoot(location, knownRootPath)}/apps/files/files`;
  return /^[1-9]\d*$/.test(documentFileId) ? `${files}/${documentFileId}${query}&opendetails` : `${files}${query}`;
}
