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

- The fallback is **silent**. If `X.Y.Z-cuda` is unpullable, AppAPI installs the
  portable image on a GPU daemon and reports success. Cassini now fails closed:
  recording remains available, but `/operator/status` reports CUDA unavailable
  and transcription stays durably queued instead of falling back to CPU. Every
  publish must still verify the `-cuda` tag is pullable *including its child
  manifests*; CI does this, and so does
  `ops/deploy/deploy-exapp.sh`. As of 2026-07-31 every published `-cuda`
  release tag is dangling.
- `<image-tag>` in the manifest is always the **base** tag. Writing
  `0.2.0-cuda` there yields a request for `0.2.0-cuda-cuda`.

### 4. The persistent volume survives updates and container recreates

`nc_app_gocassini_data` holds the job DB, the raw recordings and the published
site. An update recreates the container and remounts the same volume.

**Consequence:** a release may change code freely, but a release that changes
the *on-disk layout or schema* under that volume must migrate it forward
in-place on startup. There is no "reinstall clean" path that is not data loss.

`--rm-data` deletes the archive. Nothing in this repo passes it, and
`ops/deploy/test-exapp-register.sh` asserts that no code path can.

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
`exapp_assert_manifest_declares` in `ops/deploy/lib/exapp-register.sh` fails the
deploy instead.

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
| Driven by | `ops/deploy/deploy-exapp.sh` + a committed inventory | the Nextcloud UI |
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
path. `ops/deploy/deploy-exapp.sh` runs them detached on the target host and
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
      enforcement point

## Related

- [`exapp-install.md`](./exapp-install.md) — install, verify, Talk handoff
- [`release.md`](./release.md) — cutting and publishing a release
