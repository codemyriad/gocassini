# One-click install and update — what AppAPI can and cannot deliver

Cassini is moving towards install and update from the Nextcloud admin UI: an
admin picks Cassini in External Apps, presses **Install**, and later presses
**Update** when a new release appears. This page states the constraints that
shape both, so releases stay deliverable by button-press and the ones that are
not get flagged as such *before* they ship.

Audience: whoever is building the one-click flow, and whoever cuts releases.

## The model in one paragraph

AppAPI installs an app onto a **deploy daemon**, not onto a host. The daemon
owns the Docker engine, the network, and the compute device. The app owns its
manifest, its routes, and its deploy options. An update re-runs the deploy
against the same daemon with a newer manifest, reusing the stored deploy
options and keeping the persistent volume. Everything below follows from that.

## Rules

### 1. A remote GPU node updates exactly like a local CPU one

AppAPI targets the daemon. Whether the daemon drives a Docker socket on the
Nextcloud host or a Docker engine three network hops away behind an FRP tunnel
is the daemon's problem, not the update's. The Update button does not need to
know that Cassini's production container runs in an LXC container on a
different physical machine.

**Consequence for the builder:** there is nothing remote-specific to implement.
If update works against a local daemon it works against george.

**Consequence for testing:** conversely, a local-daemon test does not prove the
remote path. The parts that break on a remote daemon are the tunnel and the
image pull, and they break at *deploy* time, not at update-logic time.

### 2. `app_api:app:update` reuses stored deploy options; the app stays on its daemon

The update path replays what was recorded at install. It has no `--env` flag.
The app does not move.

### 3. The compute device — and therefore the `-cuda` image — comes from the daemon

The daemon carries `computeDevice`. For a CUDA daemon AppAPI tries
`<image-tag>-cuda` first and falls back to the plain tag. The admin never picks
an image variant, the manifest never names one, and no install or update flow
should offer a CPU/GPU choice — that choice was made when the daemon was
registered.

**This is the single most important design point.** A CPU deployment and a GPU
deployment differ by one daemon field. Any code path that forks on device is a
bug: it will drift, and the two halves will not be tested equally.

**Two traps:**

- The image fallback is **silent**. If `X.Y.Z-cuda` is unpullable, AppAPI
  installs the portable image on a GPU daemon and reports success — you get a
  working install that transcribes on the CPU while the GPU sits idle. Cassini
  makes that visible rather than fatal: `/operator/status` and Cassini Admin
  report `device: cpu`, and the operator logs a warning when a CUDA-capable
  image cannot see an NVIDIA device. Every publish must still verify the
  `-cuda` tag is pullable *including its child manifests*; CI does this, and so
  does `deploy-exapp.sh` (in the systems repository). As of 2026-07-31 every published `-cuda`
  release tag is dangling.
- `<image-tag>` in the manifest is always the **base** tag. Writing
  `0.2.0-cuda` there yields a request for `0.2.0-cuda-cuda`.

> **Upgrading from a GPU-only release.** Releases between the GPU-only change
> and D-702 blocked every build on a CPU daemon. Updating to this release
> restores CPU transcription: new recordings build on whichever device the host
> has, and recordings left in `build/blocked` are recovered with **Rerun** in
> Cassini Admin — no image or daemon change required. An installation that
> *wants* GPU-only behaviour pins `device_override=cuda` in Settings, which
> keeps blocking a GPU-less host instead of falling back.

### 4. The persistent volume survives updates and container recreates

`nc_app_gocassini_data` holds the job DB, the raw recordings and the published
site. An update recreates the container and remounts the same volume.

**Consequence:** a release may change code freely, but a release that changes
the *on-disk layout or schema* under that volume must migrate it forward
in-place on startup. There is no "reinstall clean" path that is not data loss.

`--rm-data` deletes the archive. Nothing in this repo passes it, and
`scripts/test-exapp-register.sh` asserts that no code path can.

### 5. Deploy env is creation-time only — so a new *required* env var is a breaking change

This is the rule most likely to be violated by accident, because it is violated
by a change to application code rather than to any deploy file.

AppAPI injects deploy options as container environment at **creation**. They are
not live config. `app_api:app:update` reuses the *stored* options and cannot add
new ones. Therefore:

> **A release that adds a new REQUIRED environment variable cannot be delivered
> by the Update button.** Existing installs will update to the new image and
> start it without the variable.

Encode it as a rule for release review:

| Change | Deliverable by Update? | What to do |
|---|---|---|
| New **optional** env var, code degrades gracefully when unset | ✅ yes | Document it in `<environment-variables>` and `exapp-install.md`; existing installs keep working |
| New **required** env var | ❌ **no — breaking** | Treat as a redeploy: bump the version accordingly, say so in the changelog and release notes, and give operators the re-register command. Do not ship it as a routine update |
| Changed *meaning* of an existing var | ❌ no | Same as above; the stored value replays with new semantics |
| Rotated secret value (e.g. Talk recording secret) | ❌ no | Update is not a rotation mechanism. Rotation is a coordinated recreate — see the rotation checklist in `exapp-install.md` |
| Removed env var | ✅ yes | The stored option becomes inert. Remove the declaration in a later release |

The safe default for new configuration is: **make it optional, with a working
default**, and read it at runtime. Cassini's own required pair
(`CASSINI_TALK_RECORDING_SECRET`, `CASSINI_TALK_SIGNALING_INTERNAL_SECRET`)
already exists on every install and is the reason this rule is known.

There is a second trap in the same area: AppAPI only passes variables **declared
in `appinfo/info.xml`** under `<environment-variables>`. Values for undeclared
keys are **silently dropped** — the install succeeds and the feature is dead.
`exapp_assert_manifest_declares` in `scripts/lib-exapp-register.sh` fails the
deploy instead.

### 5a. Routes are NOT creation-time — an in-place update rewrites them, provided the release bumps `<version>`

Rule 5 is about deploy **env**, and env is creation-time. `<routes>` is not, and
the difference is the sentence worth remembering: **an update cannot add an
environment variable, but it can and does add a route.**

This was an open question for a while, and two shipped changes depended on the
answer. The AI-provider settings surface widened `^operator\/settings\/?$` to
`^operator\/settings(\/.*)?$` so `/settings/llm/...` is reachable at all, and
insights declares the app's first mutating USER routes (`^insights\/…`). Had the
allowlist been creation-time like env, every in-place-upgraded install would 404
on both surfaces — and a 404 through the proxy says nothing about its cause.

**The answer**, measured on 2026-09-03 against a real Nextcloud with AppAPI
34.0.0: `app_api:app:update` **does** rewrite AppAPI's route allowlist, but only
when the manifest it is handed carries a **new `<version>`**. Handed the same
version it short-circuits on *"ExApp gocassini is already updated"* and the
allowlist is left exactly as it was.

The check registers the app with the real manifest **minus one real route**
(`^operator\/setup\/?$` — a route that exists, is served, and is USER-level),
proves the proxy refuses that path, then tries each update form and reads
**AppAPI's own `oc_ex_apps_routes` rows** after every one. Reading the database
rather than the proxy is load-bearing: an earlier run of the same experiment
concluded "does not refresh" from a proxy 401 that was really the container
failing its post-update init handshake.

The manifest carried 17 routes on the day this ran; the row counts below are of
that manifest, not of today's.

| Step | route rows for the app | withheld route present? | what AppAPI said |
|---|---|---|---|
| register with 16 of 17 routes (**negative control**) | 16 | no — the proxy 404s that path, while a declared one 200s | — |
| `app_api:app:update` bare | 16 | no | `Failed to get app info for gocassini from the Appstore` — a manual install is not in the store |
| `app_api:app:update --json-info`, **same** `<version>` | 16 | no | `ExApp gocassini is already updated (0.2.0-beta.6)` |
| `app_api:app:update --json-info`, **bumped** `<version>` | **17** | **yes** | `update successfully deployed` — and then the `/init` 401 below |
| re-register with the full manifest (**positive control**) | 17 | yes — proxy 200 | — |

**The rule that follows** is already the first line of the release checklist:
`scripts/bump-exapp-version.sh` bumps `<version>` and `<image-tag>` together, and
that bump is precisely what makes a route change deliverable. Stated as the
failure it prevents: *a release that changes `<routes>` without bumping
`<version>` ships a manifest nobody applies.* Nothing new to do — but a manifest
edited by hand, without the script, is now a release-blocking mistake rather than
a cosmetic one.

**One caveat, and it is about the harness rather than about routing.** After the
version-bumped update the proxy answered 401, not 200. The redeploy rotates the
app secret, and on a **manual-install** daemon AppAPI cannot recreate a
hand-started container, so the post-update `POST /init` handshake failed against
a container still holding the old secret. Restarting the container with AppAPI's
current secret gave 200 on both the withheld route and the control. That is why
the check reads the database: on a real deploy daemon the container is recreated
with the new secret and the question never arises, but on this harness the proxy
reading is not trustworthy and the route rows are.

Re-run it with `harness/bin/check-route-refresh.sh` (see `harness/README.md`); it
needs a Nextcloud stack and a built ExApp image, and it writes its verdict as a
transcript of every reading.

**Checking a pattern before you ship it.** Declaring a route is only useful if
its PCRE matches the paths it must and none that it must not — and the two traps
documented in `<routes>` (no leading slash, every internal `/` escaped as `\/`)
produce a PHP *"Unknown modifier"* error rather than a clean failure. This prints
which declared route, if any, accepts a given method and path, the same way
AppAPI's `passesExAppProxyRoutesChecks` does:

```bash
python3 - <<'PY'
import re, xml.etree.ElementTree as ET
routes = [(r.findtext('url'), r.findtext('verb'))
          for r in ET.parse('appinfo/info.xml').getroot().findall('./external-app/routes/route')]
for verb, path in [('POST', 'insights'),
                   ('GET',  'insights/ins_0123456789abcdef'),
                   ('POST', 'insights/ins_0123456789abcdef/retry'),
                   ('GET',  'insights/ins_0123456789abcdef/retry'),
                   ('POST', 'insights/ins_0123456789abcdef'),
                   ('POST', 'published/catalog.json')]:
    hits = [u for u, v in routes if re.search(u, path, re.I) and verb in v.split(',')]
    print(f'{verb:5} {path:38} {hits or "NO ROUTE -> proxy 404"}')
PY
```

For the insights block the first three lines must each name exactly one route and
the last three must say `NO ROUTE`: `[^\/]+` cannot cross a `/`, so
`^insights\/[^\/]+\/?$` reads one run and leaves `…/retry` to the route declared
after it.

### 6. Moving an app between daemons is not an update — the volume does not follow

Re-registering Cassini against a different daemon creates a **new** container on
a **new** engine with a **new** empty volume. Job history, recordings and the
published site stay on the old engine.

Production hit this on 2026-07-30 moving from `docker_aio` (cloud) to `george`:
the 54 GB archive is still sitting on cloud. Any UI that lets an admin change an
installed app's daemon must present it as a migration with data loss, not as a
setting.

There is no built-in volume migration. Moving the data means copying the volume
between engines out of band, before re-registering.

### 7. Two delivery paths, one source of truth — keep them un-conflated

| | Our managed production | Customers |
|---|---|---|
| Mechanism | direct `app_api:app:register` of a pinned image | App Store install of the published `<version>` |
| Driven by | `deploy-exapp.sh` (systems repo) + a committed inventory | the Nextcloud UI |
| Chooses the image | the operator, via `--tag` | `<image-tag>` in the released manifest |

Both read the same `appinfo/info.xml`, where `<version>` and `<image-tag>` are
held equal by CI. Release and deploy stay separate, gated workflows joined by
the version — a release publishes, it does not deploy.

The consequence for the one-click work: **the App Store path can only ever
install what a release published.** If a fix is not in a release, no button will
deliver it. Cutting the release is the deploy prerequisite, not an afterthought.

### 8. Register, enable and disable must survive a dropped client

A client-side timeout mid-handshake desyncs Nextcloud from the container. After
that, `enable` *hangs* instead of erroring — it does not fail loudly, so an
automated flow will sit there. Recovery is
`occ app_api:app:unregister <appid> --force` (which still keeps data) followed
by a re-register.

Any UI or script driving these commands needs a bounded wait plus that recovery
path. `deploy-exapp.sh` in the systems repo runs them detached on the target host and
polls, so a dropped ssh cannot cause the desync.

### 9. `PUT /enabled` is what creates the navigation entries

`app_api:app:register` sets the DB enabled flag but does not necessarily deliver
the lifecycle callback, and `app_api:app:enable` short-circuits when the flag is
already set. Cassini registers its nav entries only on receiving
`PUT /enabled?enabled=1`.

**Symptom:** the app reports `[enabled]` and has no icon. **Fix:** `disable`
then `enable`. Any install flow should do this cycle unconditionally after
register, and verify by looking for `PUT /enabled` in the container log — not by
curling `/exapps/…`, which returns 502 whether the route is right or wrong.

## Checklist for a release that wants to be one-click-updatable

- [ ] `<version>` and `<image-tag>` bumped together (`scripts/bump-exapp-version.sh`)
- [ ] No new **required** environment variable (see rule 5)
- [ ] Any new env var declared in `<environment-variables>`
- [ ] On-disk changes under `APP_PERSISTENT_STORAGE` migrate forward in place
- [ ] Published `X.Y.Z` **and** `X.Y.Z-cuda` verified pullable, child manifests
      included — a dangling `-cuda` downgrades every GPU install to CPU silently
- [ ] Route or access-level changes reviewed: the manifest's `<routes>` is the
      enforcement point, and an installed app only picks the block up because
      the release bumped `<version>` (rule 5a) — the first line of this list

## Related

- [`exapp-install.md`](./exapp-install.md) — install, verify, Talk handoff
- [`release.md`](./release.md) — cutting and publishing a release
