// readServedMessage reads an endpoint's own explanation out of a failed
// response: the JSON error envelope the operator API writes, or the plain-text
// line Go's http.Error writes. Anything longer or multi-line is a page, not a
// message, and is discarded rather than pasted into a panel. One reader for
// every client in the shell, so a served sentence reads the same everywhere.
export async function readServedMessage(response: Response): Promise<string> {
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
