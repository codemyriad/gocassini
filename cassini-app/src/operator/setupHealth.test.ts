import { describe, expect, it } from "vitest";

import {
  buildSetupNotice,
  fetchSetupHealth,
  readRecordingsAccess,
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

function accessWithMissingApps(...names: string[]): RecordingsAccess {
  return {
    ok: false,
    state: "unavailable",
    step: `app_missing:${names[0]}`,
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
    ).toEqual({ ok: false, state: "unavailable" });
    expect(called).toBe("/index.php/apps/app_api/proxy/gocassini/operator/setup");
  });

  // Routes reach AppAPI at registration time, so an app installed before this
  // route existed simply does not have it. That must read as "not asked",
  // never as "not set up" — telling a working install it is broken is a worse
  // error than the silence this replaces.
  it("returns null when the route is not registered", async () => {
    expect(await fetchSetupHealth("/operator", fetchWithJSON(404, undefined))).toBeNull();
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
    expect(readSetupHealth({ ok: true, state: "provisioned" })).toEqual({ ok: true, state: "provisioned" });
    expect(readSetupHealth({ state: "provisioned" })).toBeNull();
    expect(readSetupHealth(null)).toBeNull();
    expect(readSetupHealth([{ ok: true, state: "x" }])).toBeNull();
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
      mode: "access_controlled",
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
      mode: "",
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
      expect(stepWith(notice, "Team folders (groupfolders)").label).toContain("Everyone Group (group_everyone)");
    });

    it("gives the install command for each one", () => {
      expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
        "occ app:install groupfolders && occ app:enable groupfolders",
        "occ app:install group_everyone && occ app:enable group_everyone",
      ]);
    });

    // Setup runs on the AppAPI enabled edge, so installing the apps is only
    // half the fix — without re-firing that edge nothing re-checks (D-541).
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
    const install = stepWith(notice, "occ app:install group_everyone");
    expect(install.commands).toEqual([
      "occ app:install group_everyone && occ app:enable group_everyone",
    ]);
    expect(install.label).not.toContain("groupfolders");
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
        mode: "",
        prerequisites: [],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
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
        mode: "",
        prerequisites: [
          { name: "groupfolders", state: "enabled" },
          { name: "group_everyone", state: "enabled" },
        ],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(stepWith(notice, "CASSINI_NC_ADMIN_USER").commands).toEqual([]);
    expect(stepWith(notice, "occ app_api:app:enable gocassini")).toBeTruthy();
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
        mode: "",
        prerequisites: [
          { name: "groupfolders", state: "enabled" },
          { name: "group_everyone", state: "enabled" },
        ],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });
    expect(notice?.summary).toContain("Nothing is missing that you can install");
    expect(stepWith(notice, "nc provision:")).toBeTruthy();
    expect(JSON.stringify(notice?.steps)).not.toContain("app:install");
  });

  // The container was restarted without the app being re-enabled (D-541).
  // Publishing is already refused here, so the shell must not pretend the
  // archive is complete.
  it("explains a restart that never re-ran setup", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unknown" },
      access: { ok: false, state: "unknown", step: "", detail: "", mode: "", prerequisites: [] },
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
        access: { ok: false, state, step: "", detail: "", mode: "", prerequisites: [] },
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
      access: { ok: false, state: "something-new", step: "a_new_step", detail: "", mode: "", prerequisites: [] },
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
    expect(stepWith(notice, "occ app:install groupfolders").commands).toEqual([
      "occ app:install groupfolders && occ app:enable groupfolders",
    ]);
  });
});

// D-616 made the substrate two models, and the notice has to name the right
// prerequisite for whichever one is in force. Telling the administrator of a
// deps-free instance to install two Nextcloud apps sends them after something
// they do not need — and away from the account that is actually missing.
describe("buildSetupNotice under the default storage model", () => {
  function defaultModeAccess(step: string, detail: string): RecordingsAccess {
    return {
      ok: false,
      state: "unavailable",
      step,
      detail,
      mode: "default",
      prerequisites: [
        { name: "groupfolders", state: "missing" },
        { name: "group_everyone", state: "missing" },
      ],
    };
  }

  it("asks for the service account, not for the two apps it does not need", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: defaultModeAccess(
        "owner_account",
        'the "cassini" service account does not exist; create it with `occ user:add --group=cassini cassini`',
      ),
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(notice?.summary).toContain("service account");
    const commands = (notice?.steps ?? []).flatMap((step) => step.commands).join("\n");
    expect(commands).toContain("occ user:add --group=cassini cassini");
    expect(commands).toContain("occ group:add cassini");
    expect(commands).not.toContain("groupfolders");
    expect(commands).not.toContain("group_everyone");
  });

  // Nothing is missing here — the recorded mode and the storage simply are not
  // the same thing, and the fix is a decision rather than an install.
  it("points a mode mismatch at the Setup tab rather than at a command", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: defaultModeAccess(
        "mode_mismatch:group_folder_mount",
        'access control is off, but a "Cassini" Team folder is still mapped to a group.',
      ),
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(notice?.summary).toContain("disagree");
    expect(notice?.steps[0].label).toContain("Setup tab");
    expect((notice?.steps ?? []).flatMap((step) => step.commands)).toEqual([]);
  });

  // A missing app is still the answer when access control is the mode in force.
  it("still names the missing apps under access control", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: accessWithMissingApps("groupfolders", "group_everyone"),
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(stepWith(notice, "occ app:install groupfolders")).toBeTruthy();
  });

  // The verdict is not private; the diagnosis is. That must hold for the new
  // branches too.
  it("tells a non-administrator nothing about which account is missing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });

    expect(JSON.stringify(notice)).not.toContain("cassini service account");
    expect(JSON.stringify(notice)).not.toContain("occ");
  });
});

// D-671: the notice used to be a recipe an administrator retyped. Cassini can
// now perform most of its own setup, so the first thing it says is that there
// is a button — with the commands kept as the alternative, not deleted.
describe("buildSetupNotice offers the Setup tab", () => {
  const offer = "Setup tab";

  it("leads with the offer when the service account is missing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: {
        ok: false,
        state: "unavailable",
        step: "owner_account",
        detail: "the \"cassini\" service account does not exist",
        mode: "default",
        prerequisites: [],
      },
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(notice?.steps[0].label).toContain(offer);
    // The commands survive: an administrator who would rather run them, or
    // whose browser cannot reach Nextcloud's dialog, still needs them.
    expect(stepWith(notice, "occ user:add")).toBeTruthy();
  });

  it("leads with the offer when the native apps are missing", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: accessWithMissingApps("groupfolders", "group_everyone"),
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(notice?.steps[0].label).toContain(offer);
    expect(stepWith(notice, "occ app:install groupfolders")).toBeTruthy();
  });

  // The offer says who asks for the password, because that is the question an
  // administrator will have before clicking anything.
  it("says Nextcloud asks for the password and Cassini never sees it", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: accessWithMissingApps("groupfolders"),
      isAdmin: true,
      appUrl: APP_URL,
    });

    expect(notice?.steps[0].label).toContain("Nextcloud will ask you");
    expect(notice?.steps[0].label).toContain("never sees it");
  });

  // A non-administrator has no Setup tab, and telling them about one would be
  // pointing at a door they cannot open.
  it("offers nothing to someone who is not an administrator", () => {
    const notice = buildSetupNotice({
      health: { ok: false, state: "unavailable" },
      access: null,
      isAdmin: false,
      appUrl: APP_URL,
    });

    expect(JSON.stringify(notice)).not.toContain(offer);
  });
});
