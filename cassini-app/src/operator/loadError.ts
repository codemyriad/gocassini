import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";

import { OperatorHttpError } from "./client";

// A failed operator call, as one plain sentence (D-756, lifted from #288).
//
// The settings section used to render `error.message` — "HTTP 503", or
// whichever transport error the browser happened to throw — which tells an
// administrator nothing they can act on and reads like a fault in the thing
// they were about to change. So the status is classified into a sentence with a
// next move in it, and the raw diagnosis is kept whole, one disclosure down,
// for the bug report.
//
// The rule is the one the setup notice follows (setupHealth.ts): say what is
// true, never claim a cause that was not proven, and keep the enum names and
// the commands behind "Details for administrators".

export interface LoadError {
  // title names what failed. Rendered where this stands in for the section;
  // an inline action failure shows the summary alone.
  title: string;
  // summary is what to do about it, in one sentence.
  summary: string;
  // detail is the raw diagnosis, verbatim, for the disclosure. Never empty for
  // a classified failure: the sentence above it deliberately drops the status
  // code, and a bug report needs it back.
  detail: string;
}

// Read at call time, so it follows the reader's language.
export function loadErrorTitle(): string {
  return get(LL).app.shell.load_error.title();
}

export function buildLoadError(error: unknown): LoadError {
  const detail = error instanceof Error ? error.message : String(error);
  const t = get(LL).app.shell.load_error;
  const result: LoadError = {
    title: loadErrorTitle(),
    summary: t.summary_default(),
    detail,
  };

  if (error instanceof OperatorHttpError) {
    result.detail = `HTTP ${error.status}: ${detail}`;
    switch (error.status) {
      case 401:
        result.summary = t.summary_unauthorized();
        break;
      case 403:
        result.summary = t.summary_forbidden();
        break;
      case 404:
        // A missing route can follow an update, but it can also be a proxy or a
        // server configuration. Do not claim an outdated manifest is proven, or
        // send anybody through removing and re-adding the app on the strength
        // of a 404.
        result.summary = t.summary_not_found();
        break;
      default:
        if (error.status >= 500) {
          result.summary = t.summary_server_error();
        }
    }
  } else if (
    error instanceof TypeError &&
    /failed to fetch|fetch failed|networkerror|load failed/i.test(error.message)
  ) {
    // The browser's own words for "the request never arrived", which every
    // engine spells differently and none of them usefully.
    result.summary = t.summary_unreachable();
  }

  return result;
}
