import { describe, expect, it } from "vitest";

import {
  buildFeatureNotice,
  buildSetupNotice,
  fetchSetupHealth,
  readRecordingsAccess,
  readSetupFeatures,
  readSetupHealth,
  recordingAudience,
  shareableAppUrl,
  type RecordingsAccess,
  type SetupHealth,
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


// stepWith finds a notice step by what it says. Asserting on steps[0] couples
// every test to the ORDER of a list whose whole purpose is to grow — adding the
// Setup-tab offer broke seven of them at once.
function stepWith(notice: { steps: { label: string; commands: string[] }[] } | null, needle: string) {
  const found = notice?.steps.find(
    (step) => step.label.includes(needle) || step.commands.join("\n").includes(needle),
  );
  if (!found) {
    throw new Error(
      `no step mentioning ${JSON.stringify(needle)} in: ${JSON.stringify(notice?.steps, null, 2)}`,
    );
  }
  return found;
}

const APP_URL = "https://cloud.example.test/index.php/apps/app_api/embedded/gocassini/viewer";

// The two payloads every notice is built from, complete, so a test says only
// what it is about. accessAt is the ADMIN diagnosis from /status; setupHealth is
// the USER verdict from /setup.
function accessAt(step: string): RecordingsAccess {
  return {
    ok: false,
    state: "unavailable",
    step,
    detail: step ? `${step}: what Nextcloud answered` : "",
    // Empty: an operator older than the cause table (D-759). The branches under
    // test are this file's own fallbacks, and the operator's own sentence is
    // tested where it overrides them.
    cause: "",
    mode: "",
    modeConfirmed: true,
    prerequisites: [],
  };
}

function setupHealth(state: string, ok = false): SetupHealth {
  return { ok, state, mode: "", cause: "", features: null };
}

function accessWithMissingApps(...names: string[]): RecordingsAccess {
  return {
    ...accessAt(`app_missing:${names[0]}`),
    detail: `app_missing:${names[0]}: the "${names[0]}" app is not enabled; an ExApp cannot install it`,
    mode: "access_controlled",
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
    ).toEqual({ ok: false, state: "unavailable", mode: "", cause: "", features: null });
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
      // An absent mode reads as "", which is the right degrade for an operator
      // that predates the field: nobody said, so the chip says nothing.
      mode: "",
      // Same rule for the cause (D-759): an operator with nothing to say about
      // why is not an operator saying everything is fine.
      cause: "",
      features: null,
    });
    expect(readSetupHealth({ state: "provisioned" })).toBeNull();
    expect(readSetupHealth(null)).toBeNull();
    expect(readSetupHealth([{ ok: true, state: "x" }])).toBeNull();
  });

  // The mode is on the USER-level endpoint so the audience chip can render for
  // everybody (D-756). It is the one storage fact that is not admin detail.
  it("carries the storage mode, for the audience chip", () => {
    expect(readSetupHealth({ ok: true, state: "provisioned", mode: "access_controlled" })).toEqual({
      ok: true,
      state: "provisioned",
      mode: "access_controlled",
      cause: "",
      features: null,
    });
    // A mode that is not a string is not a mode. "" is "nobody said".
    expect(readSetupHealth({ ok: true, state: "provisioned", mode: 7 })?.mode).toBe("");
  });

  it("carries the readiness signal when the operator reports one", () => {
    expect(
      readSetupHealth({ ok: true, state: "provisioned", features: { summaries: false, insights: true } }),
    ).toEqual({
      ok: true,
      state: "provisioned",
      mode: "",
      cause: "",
      features: { summaries: false, insights: true },
    });
  });

  // The user-level half of the operator's cause table (D-759). It is a sentence
  // with no account, no path and no app in it, and the operator decides which
  // causes can be said at this level — this side only carries what arrived.
  it("carries the user-level cause when the operator sends one", () => {
    expect(
      readSetupHealth({
        ok: false,
        state: "unavailable",
        cause: "A Nextcloud app that recordings depend on is switched off.",
      })?.cause,
    ).toBe("A Nextcloud app that recordings depend on is switched off.");
    expect(readSetupHealth({ ok: false, state: "unavailable", cause: 7 })?.cause).toBe("");
  });
});

// The chip's whole job is to stop the storage enum reaching a person, and
// recordingAudience is where that translation happens (D-756). The viewing
// layer never sees `default` or `access_controlled`.
describe("recordingAudience", () => {
  it("names the two audiences, and nothing else", () => {
    expect(recordingAudience({ ok: true, state: "provisioned", mode: "default", cause: "", features: null })).toBe(
      "everyone",
    );
    expect(
      recordingAudience({ ok: true, state: "provisioned", mode: "access_controlled", cause: "", features: null }),
    ).toBe("participants");
  });

  it("says nothing when nobody said", () => {
    // A standalone export, an operator too old to report the mode, and a /setup
    // call that failed are the same three-state rule the readiness signal
    // follows: absence is not an audience.
    expect(recordingAudience(null)).toBe("");
    expect(recordingAudience({ ok: true, state: "provisioned", mode: "", cause: "", features: null })).toBe("");
    expect(
      recordingAudience({ ok: true, state: "provisioned", mode: "something_else", cause: "", features: null }),
    ).toBe("");
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
          mode: "access_controlled",
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
      cause: "",
      mode: "access_controlled",
      modeConfirmed: false,
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
      cause: "",
      mode: "",
      modeConfirmed: false,
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

// --- The notice itself (D-759) ---
//
// Every branch now says the same two things in the same order: what this costs
// you, and why it happened. The step name, the `occ` recipe and the address of
// the full report are still carried, behind "Show details", because they are
// the right content for the second screen and were never the right first
// sentence.
//
// So the tests below pin two things per branch: the sentences, verbatim, and
// the fact that nothing technical leaked into them.

const BLOCKING_TITLE = "Cassini can't save recordings right now";
const BLOCKING_SUMMARY = "Calls will still run, but their recordings will fail until this is fixed.";
const ADVISORY_TITLE = "Cassini hasn't checked that it can save recordings";
const ADVISORY_SUMMARY =
  "Recordings that are already here still open, but new ones will fail until this check runs.";

// adminNotice's branches, one fixture each. The array is what the "nothing
// technical in the opening sentences" test walks, so a branch added without a
// fixture here is a branch nobody holds to that rule.
const BRANCHES: { name: string; state: string; access: RecordingsAccess | null }[] = [
  { name: "the service account", state: "unavailable", access: accessAt("owner_account") },
  {
    name: "a declared mode this instance does not match",
    state: "unavailable",
    access: accessAt("storage_mode_declared_conflict"),
  },
  {
    name: "a mode mismatch",
    state: "unavailable",
    access: accessAt("mode_mismatch:default_root_shadowed"),
  },
  {
    name: "a missing native app",
    state: "unavailable",
    access: accessWithMissingApps("groupfolders", "group_everyone"),
  },
  { name: "no administrator to act as", state: "unavailable", access: accessAt("administrator") },
  { name: "a restart that never re-ran setup", state: "unknown", access: accessAt("") },
  {
    name: "a setup call that failed",
    state: "degraded",
    access: { ...accessAt("mount_mapping:everyone"), state: "degraded" },
  },
  { name: "a step this build does not know", state: "unavailable", access: accessAt("a_new_step") },
];

function noticeFor(state: string, access: RecordingsAccess | null) {
  return buildSetupNotice({
    health: setupHealth(state),
    access,
    isAdmin: true,
    appUrl: APP_URL,
  });
}

describe("buildSetupNotice", () => {
  it("shows nothing when the substrate is fine", () => {
    expect(
      buildSetupNotice({
        health: setupHealth("provisioned", true),
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
        health: setupHealth("not_applicable", true),
        access: null,
        isAdmin: true,
        appUrl: APP_URL,
      }),
    ).toBeNull();
  });

  it("shows nothing when the verdict could not be obtained", () => {
    expect(buildSetupNotice({ health: null, access: null, isAdmin: false, appUrl: APP_URL })).toBeNull();
  });
});

describe("what a notice opens with", () => {
  it("says the consequence first, in the same words for every fault", () => {
    for (const branch of BRANCHES) {
      const notice = noticeFor(branch.state, branch.access);
      const expected = branch.state === "unknown" ? ADVISORY_SUMMARY : BLOCKING_SUMMARY;
      expect(`${branch.name}: ${notice?.summary}`).toBe(`${branch.name}: ${expected}`);
    }
  });

  it("titles a fault as a fault and a missing check as a missing check", () => {
    expect(noticeFor("unavailable", accessAt("owner_account"))?.title).toBe(BLOCKING_TITLE);
    expect(noticeFor("unknown", accessAt(""))?.title).toBe(ADVISORY_TITLE);
  });

  // The tone says the same thing the words do. A warning triangle over "a check
  // has not run yet" is the shell shouting about the ordinary state of every
  // restarted container, which is how a notice stops being read at all.
  it("draws a fault as a warning and an unrun check as neutral", () => {
    expect(noticeFor("unavailable", accessAt("owner_account"))?.tone).toBe("warning");
    expect(noticeFor("degraded", accessAt("acl_enable"))?.tone).toBe("warning");
    expect(noticeFor("unknown", accessAt(""))?.tone).toBe("neutral");
  });

  // The rule the rewrite exists to enforce. An administrator used to be greeted
  // by `storage_mode_undecided`, a PROPFIND path and two `occ` lines; all of it
  // is still available, one level down.
  it("never opens with a step, a command, a path or the address of the report", () => {
    for (const branch of BRANCHES) {
      const notice = noticeFor(branch.state, branch.access);
      const opening = `${notice?.title} ${notice?.summary} ${notice?.cause}`;
      for (const leak of ["occ", "/status", "_", "/", "PROPFIND"]) {
        expect(`${branch.name}: ${opening}`).not.toContain(leak);
      }
    }
  });

  it("gives every branch a cause, and never the same one twice", () => {
    const causes = BRANCHES.map((branch) => noticeFor(branch.state, branch.access)?.cause ?? "");
    for (const cause of causes) {
      expect(cause.length).toBeGreaterThan(0);
      expect(cause.endsWith(".")).toBe(true);
    }
    expect(new Set(causes).size).toBe(causes.length);
  });
});

describe("the cause of each fault, in words", () => {
  it("says the service account cannot write, and what usually did that", () => {
    expect(noticeFor("unavailable", accessAt("owner_account"))?.cause).toBe(
      "Nextcloud is not letting the cassini account write to its files. This usually means the " +
        "account was removed or its group changed.",
    );
  });

  it("says a deploy option disagrees with the instance", () => {
    expect(noticeFor("unavailable", accessAt("storage_mode_declared_conflict"))?.cause).toBe(
      "A deploy option names a rule for who can see recordings that this Nextcloud does not " +
        "match, so nothing was written down.",
    );
  });

  it("says the rule and the storage disagree", () => {
    expect(noticeFor("unavailable", accessAt("mode_mismatch:default_root_shadowed"))?.cause).toBe(
      "The rule for who can see recordings and the way this Nextcloud is set up disagree, so " +
        "Cassini will not write a recording into a place the reading side is not looking.",
    );
  });

  it("says an app is switched off, without naming it in the first sentences", () => {
    const notice = noticeFor("unavailable", accessWithMissingApps("groupfolders", "group_everyone"));
    expect(notice?.cause).toBe(
      "A Nextcloud app that Cassini needs to show each person only their own recordings is " +
        "switched off, and an external app cannot install it.",
    );
    // The apps are named where they can be acted on, which is the step.
    expect(stepWith(notice, "Team folders (groupfolders)").label).toContain(
      "Everyone Group (group_everyone)",
    );
  });

  it("says no administrator could be found to act as", () => {
    expect(noticeFor("unavailable", accessAt("administrator"))?.cause).toBe(
      "Cassini could not find a Nextcloud administrator account to act as, so the recordings " +
        "folder and its permissions were never created.",
    );
  });

  // Setup runs on the AppAPI enabled edge and never at start (D-541), so this
  // is the state of every restarted container and nothing is actually wrong.
  it("says the check runs on enable, and has not run", () => {
    expect(noticeFor("unknown", accessAt(""))?.cause).toBe(
      "Cassini checks this Nextcloud when the app is enabled, and it has not been enabled " +
        "since this server started.",
    );
  });

  it("says a call failed rather than sending anyone to install something", () => {
    const notice = noticeFor("degraded", { ...accessAt("mount_mapping:everyone"), state: "degraded" });
    expect(notice?.cause).toBe(
      "A setup step failed while Cassini was talking to Nextcloud, so nothing here can prove " +
        "a recording would reach the people in the meeting.",
    );
    expect(JSON.stringify(notice?.steps)).not.toContain("app:install");
  });

  // Naming the state in the first sentence is what this rewrite removed. The
  // state and the step are still reported, in the details block.
  it("admits it does not recognise a step, without repeating the step", () => {
    const notice = noticeFor("unavailable", accessAt("a_new_step"));
    expect(notice?.cause).toBe(
      "Cassini's last check of this Nextcloud did not finish, and it did not name a reason " +
        "this version of the app understands.",
    );
    expect(notice?.cause).not.toContain("a_new_step");
  });
});

// The cause is the operator's to write (D-759): it is the only side that knows
// which check stopped, and a sentence chosen here from a step name is this file
// guessing at a diagnosis it does not have.
describe("where the cause comes from", () => {
  const OPERATOR_CAUSE = "Nextcloud stopped answering halfway through the check.";

  it("prefers the operator's own sentence over its own", () => {
    const notice = noticeFor("unavailable", { ...accessAt("owner_account"), cause: OPERATOR_CAUSE });
    expect(notice?.cause).toBe(OPERATOR_CAUSE);
  });

  // /setup carries the user-safe half of the same table. An administrator whose
  // /status predates the field still gets a sentence rather than this file's
  // guess.
  it("falls back to the user-level cause before falling back to its own", () => {
    const notice = buildSetupNotice({
      health: { ...setupHealth("unavailable"), cause: OPERATOR_CAUSE },
      access: accessAt("owner_account"),
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.cause).toBe(OPERATOR_CAUSE);
  });

  it("uses its own sentence for an operator that carries none", () => {
    expect(noticeFor("unavailable", accessAt("owner_account"))?.cause).toContain("cassini account");
  });
});

// Everything technical, one level down. Nothing was deleted in the rewrite —
// the operator's own sentence, the `occ` recipe, how to invoke `occ` and the
// address of the full report are all still on this object, and the component
// puts all four behind "Details for administrators".
describe("the details an administrator opens", () => {
  const notice = noticeFor("unavailable", accessWithMissingApps("groupfolders", "group_everyone"));

  it("quotes the operator's own sentence, so the panel and the log agree", () => {
    expect(notice?.detail).toContain("app_missing:groupfolders");
  });

  it("gives the install command for each missing app", () => {
    expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
      "occ app:install group_everyone && occ app:enable group_everyone",
    ]);
  });

  // Setup runs on the AppAPI enabled edge, so installing the apps is only half
  // the fix — without re-firing that edge nothing re-checks (D-541).
  it("tells them to re-run setup by re-enabling the app", () => {
    expect(stepWith(notice, "occ app_api:app:disable").commands).toEqual([
      "occ app_api:app:disable gocassini",
      "occ app_api:app:enable gocassini",
    ]);
  });

  it("qualifies how occ is invoked, and points at the full report", () => {
    expect(notice?.note).toContain("php occ");
    expect(notice?.reference).toContain("/operator/status");
  });

  // The state and the step used to be the first thing an administrator read.
  // They are facts a bug report needs, so they moved rather than went away.
  it("names the state and the step when the operator sent no sentence", () => {
    const detail = noticeFor("something-new", {
      ...accessAt("a_new_step"),
      state: "something-new",
      detail: "",
    })?.detail;
    expect(detail).toContain("something-new");
    expect(detail).toContain("a_new_step");
  });

  it("names only the app that is actually missing", () => {
    const oneMissing = noticeFor("unavailable", {
      ...accessWithMissingApps("group_everyone"),
      prerequisites: [
        { name: "groupfolders", state: "enabled" },
        { name: "group_everyone", state: "missing" },
      ],
    });
    const install = stepWith(oneMissing, "occ app:install group_everyone");
    expect(install.commands).toEqual([
      "occ app:install group_everyone && occ app:enable group_everyone",
    ]);
    expect(install.label).not.toContain("groupfolders");
  });

  // An operator that reported the step without the per-app list.
  it("falls back to the step when there is no per-app list", () => {
    const notice = noticeFor("unavailable", accessAt("app_missing:groupfolders"));
    expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
    ]);
  });

  it("tells an administrator with no resolvable admin account which variable to set", () => {
    const notice = noticeFor("unavailable", {
      ...accessAt("administrator"),
      detail: "administrator: no Nextcloud administrator could be resolved",
      prerequisites: [
        { name: "groupfolders", state: "enabled" },
        { name: "group_everyone", state: "enabled" },
      ],
    });
    expect(stepWith(notice, "CASSINI_NC_ADMIN_USER").commands).toEqual([]);
    expect(stepWith(notice, "occ app_api:app:enable gocassini")).toBeTruthy();
  });

  // A failed call is not an absent app: there is nothing to install, so the
  // notice must not tell an administrator to go looking for one.
  it("sends an administrator to the log when a setup call failed", () => {
    const notice = noticeFor("degraded", {
      ...accessAt("mount_mapping:everyone"),
      state: "degraded",
      detail: "mount_mapping:everyone: POST -> 500",
      prerequisites: [
        { name: "groupfolders", state: "enabled" },
        { name: "group_everyone", state: "enabled" },
      ],
    });
    expect(stepWith(notice, "nc provision:")).toBeTruthy();
  });

  // The container was restarted without the app being re-enabled (D-541).
  it("offers the one step that re-runs the check after a restart", () => {
    const notice = noticeFor("unknown", accessAt(""));
    expect(notice?.steps).toHaveLength(1);
    expect(notice?.steps[0].commands).toEqual([
      "occ app_api:app:disable gocassini",
      "occ app_api:app:enable gocassini",
    ]);
  });

  it("does not offer an administrator a link to send themselves", () => {
    expect(notice?.shareUrl).toBe("");
  });
});

// D-671 gave the notice a button instead of a recipe. D-756 took away the tab
// the button opened, and D-757 put the controls in Operator › Settings, so the
// offer names that and the prose stops sending anybody to a tab that is gone.
describe("buildSetupNotice offers Operator › Settings", () => {
  const OFFER = "Operator › Settings";

  it("leads with the offer when the service account is missing", () => {
    const notice = noticeFor("unavailable", accessAt("owner_account"));
    expect(notice?.steps[0].label).toContain(OFFER);
    expect(notice?.steps[0].action).toBe("settings");
    // The commands survive: an administrator who would rather run them, or
    // whose browser cannot reach Nextcloud's dialog, still needs them.
    expect(stepWith(notice, "occ user:add")).toBeTruthy();
  });

  it("leads with the offer when the native apps are missing", () => {
    const notice = noticeFor("unavailable", accessWithMissingApps("groupfolders", "group_everyone"));
    expect(notice?.steps[0].label).toContain(OFFER);
    expect(stepWith(notice, "occ app:install groupfolders")).toBeTruthy();
  });

  // The offer says who asks for the password, because that is the question an
  // administrator will have before pressing anything.
  it("says Nextcloud asks for the password and Cassini never sees it", () => {
    const notice = noticeFor("unavailable", accessWithMissingApps("groupfolders"));
    expect(notice?.steps[0].label).toContain("Nextcloud will ask you");
    expect(notice?.steps[0].label).toContain("never sees it");
  });

  // The tab is gone (D-756). A step that still named it would send an
  // administrator looking for something that does not exist.
  it("never names the Setup tab, in any branch", () => {
    for (const branch of BRANCHES) {
      expect(JSON.stringify(noticeFor(branch.state, branch.access))).not.toContain("Setup tab");
    }
  });

  // A non-administrator has no operator surface, and telling them about one
  // would be pointing at a door they cannot open.
  it("offers nothing to someone who is not an administrator", () => {
    const notice = buildSetupNotice({
      health: setupHealth("unavailable"),
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });
    expect(JSON.stringify(notice)).not.toContain(OFFER);
  });
});

describe("for someone who is not an administrator", () => {
  const notice = buildSetupNotice({
    health: setupHealth("unavailable"),
    access: null,
    isAdmin: false,
    appUrl: APP_URL,
  });

  // The same first sentence an administrator gets. The consequence is not
  // privileged — it is what this person is living with — and it is the whole of
  // what they are told.
  it("says the same first sentence, and no second one", () => {
    expect(notice?.title).toBe(BLOCKING_TITLE);
    expect(notice?.summary).toBe(BLOCKING_SUMMARY);
    expect(notice?.cause).toBe("");
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
    expect(notice?.note).toBe("");
    expect(JSON.stringify(notice)).not.toContain("occ");
    expect(JSON.stringify(notice)).not.toContain("groupfolders");
    expect(JSON.stringify(notice)).not.toContain("cassini account");
  });

  // Even when the operator sent a user-level cause, and even when a /status
  // body somehow reached this call: what makes a notice an administrator's is
  // the audience, not the payload.
  it("stays one sentence even when a cause arrived", () => {
    const withCause = buildSetupNotice({
      health: {
        ...setupHealth("unavailable"),
        cause: "A Nextcloud app that recordings depend on is switched off.",
      },
      access: accessWithMissingApps("groupfolders"),
      isAdmin: false,
      appUrl: APP_URL,
    });
    expect(withCause?.summary).toBe(BLOCKING_SUMMARY);
    expect(withCause?.cause).toBe("");
    expect(withCause?.steps).toEqual([]);
  });

  it("tells them the archive is still there when nothing is actually broken", () => {
    const advisory = buildSetupNotice({
      health: setupHealth("unknown"),
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });
    expect(advisory?.title).toBe(ADVISORY_TITLE);
    expect(advisory?.summary).toBe(ADVISORY_SUMMARY);
    expect(advisory?.shareUrl).toBe(APP_URL);
  });
});

// D-616 made the substrate two models, and the notice has to name the right
// prerequisite for whichever one is in force. Telling the administrator of a
// deps-free instance to install two Nextcloud apps sends them after something
// they do not need — and away from the account that is actually missing.
describe("buildSetupNotice under the default storage model", () => {
  function defaultModeAccess(step: string, detail: string): RecordingsAccess {
    return {
      ...accessAt(step),
      detail,
      mode: "default",
      prerequisites: [
        { name: "groupfolders", state: "missing" },
        { name: "group_everyone", state: "missing" },
      ],
    };
  }

  it("asks for the service account, not for the two apps it does not need", () => {
    const notice = noticeFor(
      "unavailable",
      defaultModeAccess(
        "owner_account",
        'the "cassini" service account does not exist; create it with `occ user:add --group=cassini cassini`',
      ),
    );

    expect(notice?.cause).toContain("cassini account");
    const commands = (notice?.steps ?? []).flatMap((step) => step.commands).join("\n");
    expect(commands).toContain("occ user:add --group=cassini cassini");
    expect(commands).toContain("occ group:add cassini");
    expect(commands).not.toContain("groupfolders");
    expect(commands).not.toContain("group_everyone");
  });

  // Nothing is missing here — the recorded rule and the storage simply are not
  // the same thing, and the fix is a decision rather than an install.
  it("points a mode mismatch at Operator › Settings rather than at a command", () => {
    const notice = noticeFor(
      "unavailable",
      defaultModeAccess(
        "mode_mismatch:group_folder_mount",
        'access control is off, but a "Cassini" Team folder is still mapped to a group.',
      ),
    );

    expect(notice?.cause).toContain("disagree");
    expect(notice?.steps[0].label).toContain("Operator › Settings");
    expect(notice?.steps[0].action).toBe("settings");
    expect((notice?.steps ?? []).flatMap((step) => step.commands)).toEqual([]);
  });

  // A missing app is still the answer when access control is the rule in force.
  it("still names the missing apps under access control", () => {
    const notice = noticeFor("unavailable", accessWithMissingApps("groupfolders", "group_everyone"));
    expect(stepWith(notice, "occ app:install groupfolders")).toBeTruthy();
  });
});

// The distinction that keeps this from being a worse bug than the one it fixes.
// The read path never consults the provisioning record — it fetches as the
// caller — so "setup is unproven" and "nothing can be read" are different facts,
// and only the second may take the meeting list away.
describe("blocking", () => {
  function blockingFor(state: string, isAdmin: boolean): boolean | undefined {
    return buildSetupNotice({
      health: setupHealth(state),
      access: { ...accessAt(""), state },
      isAdmin,
      appUrl: APP_URL,
    })?.blocking;
  }

  // Setup runs on the AppAPI enabled edge, never at start, so ANY container
  // restart of a perfectly provisioned instance reports "unknown". Every
  // recording still opens; blocking here would blank a working archive for the
  // whole instance on every reboot.
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
  expect(notice?.title).toBe(BLOCKING_TITLE);
  expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
    "occ app:install groupfolders && occ app:enable groupfolders",
  ]);
});

// There is no "somebody has to decide" state any more (D-756): the operator
// resolves the mode when it is enabled and records from then on. The steps an
// older operator still reports for it are not a question the app asks, so they
// get the ordinary fault branch — and, since D-759, without the enum name.
describe("buildSetupNotice when the operator resolved the mode itself", () => {
  it("says nothing to anybody on an install that is simply working", () => {
    const health = setupHealth("provisioned", true);
    expect(buildSetupNotice({ health, access: null, isAdmin: true, appUrl: APP_URL })).toBeNull();
    expect(buildSetupNotice({ health, access: null, isAdmin: false, appUrl: APP_URL })).toBeNull();
  });

  it("does not invite a non-administrator to go and find an administrator to decide", () => {
    const notice = buildSetupNotice({
      health: { ...setupHealth("unavailable"), mode: "default" },
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });
    expect(notice?.title).toBe(BLOCKING_TITLE);
    expect(notice?.summary).not.toContain("choose");
    expect(notice?.summary).not.toContain("decide");
    expect(notice?.shareLabel).toContain("exactly what is missing");
  });

  it("treats a leftover undecided step as an ordinary fault, and does not print it", () => {
    const notice = noticeFor("unavailable", {
      ...accessAt("storage_mode_undecided"),
      detail: "nobody has chosen where Cassini keeps recordings",
    });
    expect(notice?.title).toBe(BLOCKING_TITLE);
    expect(notice?.summary).toBe(BLOCKING_SUMMARY);
    expect(notice?.cause).not.toContain("storage_mode_undecided");
    // …and it blocks, like every other unreadable substrate: an unmade decision
    // is no longer the carve-out that kept the list on screen. The step itself
    // is still reported, in the operator's own sentence, in the details.
    expect(notice?.blocking).toBe(true);
    expect(notice?.detail).toBe("nobody has chosen where Cassini keeps recordings");
  });

  it("treats a leftover unconfirmed step the same way", () => {
    const notice = noticeFor("unavailable", {
      ...accessAt("storage_mode_unconfirmed"),
      detail: "an earlier version recorded it without asking",
      mode: "default",
    });
    expect(notice?.summary).toBe(BLOCKING_SUMMARY);
    expect(notice?.cause).not.toContain("storage_mode_unconfirmed");
    expect(notice?.detail).toBe("an earlier version recorded it without asking");
  });
});
