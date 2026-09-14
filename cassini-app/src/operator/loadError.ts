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

export const LOAD_ERROR_TITLE = "Cassini couldn't read these settings";

export function buildLoadError(error: unknown): LoadError {
  const detail = error instanceof Error ? error.message : String(error);
  const result: LoadError = {
    title: LOAD_ERROR_TITLE,
    summary:
      "Try again in a moment. If it still won't load, share the technical details below with " +
      "Cassini support.",
    detail,
  };

  if (error instanceof OperatorHttpError) {
    result.detail = `HTTP ${error.status}: ${detail}`;
    switch (error.status) {
      case 401:
        result.summary = "Sign in to Nextcloud again, then try again.";
        break;
      case 403:
        result.summary =
          "Nextcloud denied access. Make sure you're signed in with an administrator account, " +
          "then try again.";
        break;
      case 404:
        // A missing route can follow an update, but it can also be a proxy or a
        // server configuration. Do not claim an outdated manifest is proven, or
        // send anybody through removing and re-adding the app on the strength
        // of a 404.
        result.summary =
          "Cassini's storage settings couldn't be found. If you just updated Cassini, check that " +
          "the update has finished, then try again.";
        break;
      default:
        if (error.status >= 500) {
          result.summary = "Cassini couldn't respond right now. Wait a moment and try again.";
        }
    }
  } else if (
    error instanceof TypeError &&
    /failed to fetch|fetch failed|networkerror|load failed/i.test(error.message)
  ) {
    // The browser's own words for "the request never arrived", which every
    // engine spells differently and none of them usefully.
    result.summary = "Cassini couldn't be reached. Check your connection and try again.";
  }

  return result;
}
