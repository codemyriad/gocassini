import { OperatorHttpError } from "./client";

export interface SetupLoadError {
  title: string;
  summary: string;
  detail: string;
}

// The first storage request happens before the wizard can render. Keep this
// failure actionable too, without presenting a transport error as the cause
// of an incomplete installation.
export function buildSetupLoadError(error: unknown): SetupLoadError {
  const detail = error instanceof Error ? error.message : String(error);
  const result: SetupLoadError = {
    title: "We couldn’t open setup",
    summary:
      "Try again in a moment. If setup still won’t open, share the technical details below with Cassini support.",
    detail,
  };

  if (error instanceof OperatorHttpError) {
    result.detail = `HTTP ${error.status}: ${detail}`;
    switch (error.status) {
      case 401:
        result.summary = "Sign in to Nextcloud again, then try opening setup.";
        break;
      case 403:
        result.summary =
          "Nextcloud denied access to setup. Make sure you’re signed in with an administrator account, then try again.";
        break;
      case 404:
        // A missing route can follow an upgrade, but can also be a proxy or
        // server configuration issue. Do not claim an outdated manifest is
        // proven, or send people through removal/reinstallation from a 404.
        result.summary =
          "The setup service couldn’t be found. If you just upgraded Cassini, check that the update has finished, then try again.";
        break;
      default:
        if (error.status >= 500) {
          result.summary = "Cassini couldn’t respond right now. Wait a moment and try again.";
        }
    }
  } else if (
    error instanceof TypeError &&
    /failed to fetch|fetch failed|networkerror|load failed/i.test(error.message)
  ) {
    result.summary = "Cassini couldn’t be reached. Check your connection and try again.";
  }

  return result;
}
