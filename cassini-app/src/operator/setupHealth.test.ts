import { describe, expect, it } from "vitest";

import {
  buildFeatureNotice,
  buildSetupNotice,
  fetchSetupHealth,
  readRecordingsAccess,
  readSetupFeatures,
  readSetupHealth,
  shareableAppUrl,
  type RecordingsAccess,
} from "./setupHealth";

function fetchWithJSON(status: number, body: unknown, capture?: (url: string) => void): typeof fetch {
  return (async (url: string) => {
    capture?.(url);
    return {
      status,
      json: async () => {
        if (body === undefined) {
          throw new Error("no body");
        }
        return body;
      },
    } as Response;
  }) as unknown as typeof fetch;
}

const APP_URL = "https://cloud.example.test/index.php/apps/app_api/embedded/gocassini/viewer";

function accessWithMissingApps(...names: string[]): RecordingsAccess {
  return {
    ok: false,
    state: "unavailable",
    step: `app_missing:${names[0]}`,
    detail: `app_missing:${names[0]}: the "${names[0]}" app is not enabled; an ExApp cannot install it`,
    prerequisites: names.map((name) => ({ name, state: "missing" })),
  };
}

describe("fetchSetupHealth", () => {
  it("reads the verdict from <base>/setup", async () => {
    let called = "";
    expect(
      await fetchSetupHealth(
        "/index.php/apps/app_api/proxy/gocassini/operator/",
        fetchWithJSON(200, { ok: false, state: "unavailable" }, (u) => (called = u)),
      ),
    ).toEqual({ ok: false, state: "unavailable", features: null });
    expect(called).toBe("/index.php/apps/app_api/proxy/gocassini/operator/setup");
  });

  // Routes reach AppAPI at registration time, so an app installed before this
  // route existed simply does not have it. That must read as "not asked",
  // never as "not set up" — telling a working install it is broken is a worse
  // error than the silence this replaces.
  it("returns null when the route is not registered", async () => {
    expect(await fetchSetupHealth("/operator", fetchWithJSON(404, undefined))).toBeNull();
  });

  // AppAPI caches a proxied GET for an hour. Both answers this route carries
  // change the moment an administrator acts, and a cached one would have the
  // app insisting the deployment is unconfigured long after it was configured.
  it("does not read a cached answer", async () => {
    let init: RequestInit | undefined;
    const capturing = (async (_url: string, options: RequestInit) => {
      init = options;
      return { status: 200, json: async () => ({ ok: true, state: "provisioned" }) } as Response;
    }) as unknown as typeof fetch;
    await fetchSetupHealth("/operator", capturing);
    expect(init?.cache).toBe("no-store");
  });

  it("returns null on a transport error", async () => {
    const throwing = (async () => {
      throw new Error("network down");
    }) as unknown as typeof fetch;
    expect(await fetchSetupHealth("/operator", throwing)).toBeNull();
  });

  it("returns null for a body that is not the setup verdict", async () => {
    expect(await fetchSetupHealth("/operator", fetchWithJSON(200, "<html>proxy error</html>"))).toBeNull();
    expect(await fetchSetupHealth("/operator", fetchWithJSON(200, { ok: "yes" }))).toBeNull();
  });
});

describe("readSetupHealth", () => {
  it("accepts the ok+state pair and nothing else", () => {
    expect(readSetupHealth({ ok: true, state: "provisioned" })).toEqual({
      ok: true,
      state: "provisioned",
      features: null,
    });
    expect(readSetupHealth({ state: "provisioned" })).toBeNull();
    expect(readSetupHealth(null)).toBeNull();
    expect(readSetupHealth([{ ok: true, state: "x" }])).toBeNull();
  });

  it("carries the readiness signal when the operator reports one", () => {
    expect(
      readSetupHealth({ ok: true, state: "provisioned", features: { summaries: false, insights: true } }),
    ).toEqual({ ok: true, state: "provisioned", features: { summaries: false, insights: true } });
  });
});

describe("readSetupFeatures", () => {
  // An operator that predates D-722 answers ok+state and nothing else. That has
  // to read as "did not say", never as "nothing is configured": telling a
  // deployment that summarises perfectly well that it does not is the same
  // class of error the setup notice exists to avoid.
  it("is null for an operator that does not report it", () => {
    expect(readSetupFeatures(undefined)).toBeNull();
    expect(readSetupFeatures(null)).toBeNull();
  });

  it("insists on both bits, as booleans", () => {
    expect(readSetupFeatures({ summaries: true, insights: true })).toEqual({
      summaries: true,
      insights: true,
    });
    // Half an answer is an operator this build does not understand; guessing
    // the other half is how a working deployment gets told it is broken.
    expect(readSetupFeatures({ insights: true })).toBeNull();
    expect(readSetupFeatures({ summaries: "yes", insights: true })).toBeNull();
  });
});

describe("buildFeatureNotice", () => {
  const CONFIGURED = { summaries: true, insights: true };
  const NOTHING = { summaries: false, insights: false };

  it("says nothing when the capability is there", () => {
    expect(buildFeatureNotice({ features: CONFIGURED, feature: "insights", isAdmin: true })).toBeNull();
    expect(buildFeatureNotice({ features: CONFIGURED, feature: "summaries", isAdmin: false })).toBeNull();
  });

  // The standalone export has no operator to ask, so nobody answered — and an
  // unanswered question must not render as "not configured". Same three-state
  // rule the catalog's hasSummary follows.
  it("says nothing when nobody answered", () => {
    expect(buildFeatureNotice({ features: null, feature: "insights", isAdmin: false })).toBeNull();
    expect(buildFeatureNotice({ features: null, feature: "summaries", isAdmin: true })).toBeNull();
  });

  it("offers an administrator the panel that fixes it", () => {
    const notice = buildFeatureNotice({ features: NOTHING, feature: "insights", isAdmin: true });
    expect(notice?.panel).toBe("endpoints");
    expect(notice?.actionLabel).not.toBe("");
  });

  // The whole point of the split: the AI settings panel is ADMIN at the proxy
  // and its PUT would 403, so a non-admin offered that button is offered a way
  // to fail. They get the fact and who can act on it — buildSetupNotice's
  // precedent, and the only remedy actually available to them.
  it("offers a non-admin no control they cannot use", () => {
    const notice = buildFeatureNotice({ features: NOTHING, feature: "insights", isAdmin: false });
    expect(notice?.panel).toBe("");
    expect(notice?.actionLabel).toBe("");
    expect(notice?.summary).toContain("administrator");
  });

  it("names the two gaps separately", () => {
    // They are different facts — an endpoint can exist with summarising off —
    // so neither sentence may be reachable from the other's state.
    const summaries = buildFeatureNotice({
      features: { summaries: false, insights: true },
      feature: "summaries",
      isAdmin: true,
    });
    const insights = buildFeatureNotice({
      features: { summaries: false, insights: false },
      feature: "insights",
      isAdmin: true,
    });
    expect(summaries?.title).not.toBe(insights?.title);
    // Both promise the local half is unaffected, because it is: transcription
    // needs no endpoint, and docs/privacy.md is the claim being upheld here.
    expect(summaries?.summary).toContain("Transcripts are unaffected");
    expect(insights?.summary).toContain("Recording and transcription are unaffected");
  });
});

describe("readRecordingsAccess", () => {
  it("pulls the diagnosis out of a /status body", () => {
    expect(
      readRecordingsAccess({
        ok: false,
        recordings_access: {
          ok: false,
          state: "unavailable",
          step: "app_missing:group_everyone",
          detail: "app_missing:group_everyone: the app is not enabled",
          prerequisites: [
            { name: "groupfolders", state: "enabled" },
            { name: "group_everyone", state: "missing" },
          ],
        },
      }),
    ).toEqual({
      ok: false,
      state: "unavailable",
      step: "app_missing:group_everyone",
      detail: "app_missing:group_everyone: the app is not enabled",
      prerequisites: [
        { name: "groupfolders", state: "enabled" },
        { name: "group_everyone", state: "missing" },
      ],
    });
  });

  it("returns null for anything that is not a status payload", () => {
    expect(readRecordingsAccess({ ok: true })).toBeNull();
    expect(readRecordingsAccess("<html>Bad gateway</html>")).toBeNull();
    expect(readRecordingsAccess(null)).toBeNull();
  });

  it("tolerates a payload missing the optional halves", () => {
    expect(readRecordingsAccess({ recordings_access: { state: "unknown" } })).toEqual({
      ok: false,
      state: "unknown",
      step: "",
      detail: "",
      prerequisites: [],
    });
  });
});

describe("shareableAppUrl", () => {
  it("drops the fragment, which is the viewer's deep link and not the app", () => {
    expect(shareableAppUrl(`${APP_URL}#meeting=2026-08-04-standup&t=91`)).toBe(APP_URL);
  });

  it("returns the input unchanged when it cannot be parsed", () => {
    expect(shareableAppUrl("not a url")).toBe("not a url");
  });
});

describe("buildSetupNotice", () => {
  it("shows nothing when the substrate is fine", () => {
    expect(
      buildSetupNotice({
        health: { ok: true, state: "provisioned" },
        access: null,
        isAdmin: false,
        appUrl: APP_URL,
      }),
    ).toBeNull();
  });

  // A standalone operator, or an ExApp pinned to CASSINI_PUBLISH_SINK=local,
  // serves no recordings from Nextcloud Files and cannot be broken for want of
  // a substrate it never uses.
  it("shows nothing when no substrate is expected", () => {
    expect(
      buildSetupNotice({
        health: { ok: true, state: "not_applicable" },
        access: null,
        isAdmin: true,
        appUrl: APP_URL,
      }),
    ).toBeNull();
  });

  it("shows nothing when the verdict could not be obtained", () => {
    expect(buildSetupNotice({ health: null, access: null, isAdmin: false, appUrl: APP_URL })).toBeNull();
  });

  describe("for someone who is not an administrator", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });

    it("says it is Cassini's configuration, not their account", () => {
      expect(notice?.summary).toContain("administrator");
      expect(notice?.summary).toContain("nothing wrong with your account");
    });

    it("hands them the one thing they can do: a link to give an administrator", () => {
      expect(notice?.shareUrl).toBe(APP_URL);
      expect(notice?.shareLabel).toContain("administrator");
    });

    // The whole reason /setup exists as a separate, USER-level endpoint: the
    // verdict is not private, the diagnosis is.
    it("names no app, no step, no command", () => {
      expect(notice?.steps).toEqual([]);
      expect(notice?.detail).toBe("");
      expect(notice?.reference).toBe("");
      expect(JSON.stringify(notice)).not.toContain("occ");
      expect(JSON.stringify(notice)).not.toContain("groupfolders");
    });
  });

  describe("for an administrator whose prerequisites are missing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: accessWithMissingApps("groupfolders", "group_everyone"),
      isAdmin: true,
      appUrl: APP_URL,
    });

    it("names both apps by their Nextcloud name and their id", () => {
      expect(notice?.steps[0].label).toContain("Team folders (groupfolders)");
      expect(notice?.steps[0].label).toContain("Everyone Group (group_everyone)");
    });

    it("gives the install command for each one", () => {
      expect(notice?.steps[0].commands).toEqual([
        "occ app:install groupfolders && occ app:enable groupfolders",
        "occ app:install group_everyone && occ app:enable group_everyone",
      ]);
    });

    // Setup runs on the AppAPI enabled edge, so installing the apps is only
    // half the fix — without re-firing that edge nothing re-checks (D-541).
    it("tells them to re-run setup by re-enabling the app", () => {
      expect(notice?.steps[1].commands).toEqual([
        "occ app_api:app:disable gocassini",
        "occ app_api:app:enable gocassini",
      ]);
    });

    it("qualifies how occ is invoked, and points at the full report", () => {
      expect(notice?.note).toContain("php occ");
      expect(notice?.reference).toContain("/operator/status");
    });

    it("quotes the operator's own sentence, so the panel and the log agree", () => {
      expect(notice?.detail).toContain("app_missing:groupfolders");
    });

    it("does not offer them a link to send themselves", () => {
      expect(notice?.shareUrl).toBe("");
    });
  });

  it("names only the app that is actually missing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: {
        ...accessWithMissingApps("group_everyone"),
        prerequisites: [
          { name: "groupfolders", state: "enabled" },
          { name: "group_everyone", state: "missing" },
        ],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.steps[0].commands).toEqual([
      "occ app:install group_everyone && occ app:enable group_everyone",
    ]);
    expect(notice?.steps[0].label).not.toContain("groupfolders");
  });

  // An operator that reported the step without the per-app list.
  it("falls back to the step when there is no per-app list", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: {
        ok: false,
        state: "unavailable",
        step: "app_missing:groupfolders",
        detail: "",
        prerequisites: [],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.steps[0].commands).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
    ]);
  });

  it("tells an administrator with no resolvable admin account which variable to set", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: {
        ok: false,
        state: "unavailable",
        step: "administrator",
        detail: "administrator: no Nextcloud administrator could be resolved",
        prerequisites: [
          { name: "groupfolders", state: "enabled" },
          { name: "group_everyone", state: "enabled" },
        ],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.steps[0].label).toContain("CASSINI_NC_ADMIN_USER");
    expect(notice?.steps[0].commands).toEqual([]);
    expect(notice?.steps[1].commands).toContain("occ app_api:app:enable gocassini");
  });

  // A failed call is not an absent app: there is nothing to install, so the
  // notice must not tell an administrator to go looking for one.
  it("sends an administrator to the log when a setup call failed", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "degraded" },
      access: {
        ok: false,
        state: "degraded",
        step: "mount_mapping:everyone",
        detail: "mount_mapping:everyone: POST -> 500",
        prerequisites: [
          { name: "groupfolders", state: "enabled" },
          { name: "group_everyone", state: "enabled" },
        ],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.summary).toContain("Nothing is missing that you can install");
    expect(notice?.steps[0].label).toContain("nc provision:");
    expect(JSON.stringify(notice?.steps)).not.toContain("app:install");
  });

  // The container was restarted without the app being re-enabled (D-541).
  // Publishing is already refused here, so the shell must not pretend the
  // archive is complete.
  it("explains a restart that never re-ran setup", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unknown" },
      access: { ok: false, state: "unknown", step: "", detail: "", prerequisites: [] },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.summary).toContain("since it last restarted");
    expect(notice?.steps).toHaveLength(1);
    expect(notice?.steps[0].commands).toEqual([
      "occ app_api:app:disable gocassini",
      "occ app_api:app:enable gocassini",
    ]);
  });

  // The distinction that keeps this from being a worse bug than the one it
  // fixes. The read path never consults the provisioning record — it fetches as
  // the caller — so "setup is unproven" and "nothing can be read" are different
  // facts, and only the second may take the meeting list away.
  describe("blocking", () => {
    function blockingFor(state: string, isAdmin: boolean): boolean | undefined {
      return buildSetupNotice({
        health: { ok: false, state },
        access: { ok: false, state, step: "", detail: "", prerequisites: [] },
        isAdmin,
        appUrl: APP_URL,
      })?.blocking;
    }

    // Setup runs on the AppAPI enabled edge, never at start, so ANY container
    // restart of a perfectly provisioned instance reports "unknown". Every
    // recording still opens; blocking here would blank a working archive for
    // the whole instance on every reboot.
    it("does not take the list away after a plain restart", () => {
      expect(blockingFor("unknown", true)).toBe(false);
      expect(blockingFor("unknown", false)).toBe(false);
    });

    it("takes the list away when nothing can be read", () => {
      expect(blockingFor("unavailable", true)).toBe(true);
      expect(blockingFor("unavailable", false)).toBe(true);
      expect(blockingFor("degraded", true)).toBe(true);
      expect(blockingFor("degraded", false)).toBe(true);
    });

    // An unrecognised state is not evidence that reading works.
    it("blocks on a state it does not recognise", () => {
      expect(blockingFor("something-new", true)).toBe(true);
    });

    it("tells a non-admin their existing recordings are still there", () => {
      const advisory = buildSetupNotice({
        health: { ok: false, state: "unknown" },
        access: null,
        isAdmin: false,
        appUrl: APP_URL,
      });
      expect(advisory?.summary).toContain("still listed below");
      expect(advisory?.shareUrl).toBe(APP_URL);
    });
  });

  it("names the state rather than guessing when it recognises nothing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "something-new" },
      access: { ok: false, state: "something-new", step: "a_new_step", detail: "", prerequisites: [] },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.summary).toContain("something-new");
    expect(notice?.summary).toContain("a_new_step");
  });

  // An install whose manifest predates /setup: the verdict has to come from
  // wherever it can be had, or an administrator gets no notice at all.
  it("falls back to the /status verdict when /setup could not be reached", () => {
    const notice = buildSetupNotice({
      health: null,
      access: accessWithMissingApps("groupfolders"),
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.steps[0].commands).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
    ]);
  });
});
