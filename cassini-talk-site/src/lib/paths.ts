const rawBase = import.meta.env.BASE_URL;
const base = rawBase.endsWith("/") ? rawBase : rawBase + "/";

/** A site path that respects BASE_PATH. `href("download")` → "/download". */
export const href = (path = "") => base + path.replace(/^\//, "");
