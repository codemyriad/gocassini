import { describe, expect, it } from "vitest";
import { OperatorHttpError } from "./client";
import { buildSetupLoadError } from "./setupLoadError";

describe("setup loading errors", () => {
  it("suggests an upgrade check for a missing route without claiming its cause", () => {
    const error = buildSetupLoadError(new OperatorHttpError(404, "route /operator/storage not found"));
    expect(error.summary).toContain("couldn’t be found");
    expect(error.summary).toContain("If you just upgraded");
    expect(error.summary).not.toMatch(/re-register|remove|reinstall|manifest/);
    expect(error.detail).toBe("HTTP 404: route /operator/storage not found");
  });

  it.each(["Failed to fetch", "fetch failed", "NetworkError when attempting to fetch resource.", "Load failed"])(
    "offers a connection check for browser network failure: %s",
    (message) => {
      const error = buildSetupLoadError(new TypeError(message));
      expect(error.summary).toContain("Check your connection and try again");
      expect(error.detail).toBe(message);
    },
  );

  it("offers an account check for forbidden access without claiming the account is at fault", () => {
    const error = buildSetupLoadError(new OperatorHttpError(403, "Forbidden"));
    expect(error.summary).toContain("denied access");
    expect(error.summary).toContain("Make sure you’re signed in with an administrator account");
    expect(error.detail).toBe("HTTP 403: Forbidden");
  });

  it("asks for a fresh sign-in when authentication is required", () => {
    const error = buildSetupLoadError(new OperatorHttpError(401, "Unauthorized"));
    expect(error.summary).toContain("Sign in to Nextcloud again");
    expect(error.detail).toBe("HTTP 401: Unauthorized");
  });

  it("offers retry for a server failure while preserving the diagnosis", () => {
    const error = buildSetupLoadError(new OperatorHttpError(503, "upstream unavailable"));
    expect(error.summary).toContain("Wait a moment and try again");
    expect(error.detail).toBe("HTTP 503: upstream unavailable");
  });

  it.each([new Error("private server diagnosis"), new TypeError("unexpected value"), "unknown response"])(
    "keeps an unclassified diagnosis in technical details only: %s",
    (cause) => {
      const error = buildSetupLoadError(cause);
      const message = cause instanceof Error ? cause.message : cause;
      expect(error.summary).toContain("Try again");
      expect(error.summary).toContain("Cassini support");
      expect(error.summary).not.toContain(message);
      expect(error.detail).toBe(message);
    },
  );
});
