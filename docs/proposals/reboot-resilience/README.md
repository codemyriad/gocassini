# Reboot resilience: "settings reset at reboot", diagnosed, with a removal-first change set

Baseline: `a541980f` (equal to `origin/main`). All `file:line` citations are at that commit.
Terms used below:

- **AIO**: Nextcloud All-in-One, a Nextcloud distribution whose startup script reconfigures apps on every start.
- **Sidecar**: a small JSON state file next to `jobs.sqlite3` in the operator directory (for example `settings.json`).
- **Stand-in policy**: the in-memory defaults Cassini runs on when `settings.json` cannot be loaded. Transcription is off and the share audience is `room`.
- **Storage verdict**: Cassini's cached answer to "can I save recordings into Nextcloud?" (`provisioned`, `degraded` or `unavailable`).

## 1. Summary

A 0.2.0 user who shuts the server down nightly to copy a backup reports that "Cassini settings keep resetting at reboot". We have no logs and do not know which setting changed. A clean shutdown on the default AppAPI volume does not lose Cassini's files, so the most likely causes are things that *look* like a reset. The likeliest (medium confidence, if they run AIO) is AIO's startup script, which deletes or replaces Talk's recording backend on every Nextcloud start, so the admin must press Connect Talk again each morning. The next two (medium) are reboot-specific behaviour in Cassini: every host reboot marks every installed model as not ready, because the model fingerprint includes the kernel `boot_id`; and a storage check that runs before Nextcloud is up can leave a false "needs an administrator" banner. A lower-confidence cause is a settings load at boot that rewrites `settings.json`, or falls back to the stand-in policy when that write fails. Real loss of Cassini state needs a non-persistent volume, a recreate, or a partial restore. The fix is mostly subtractive: settings reads never write, loads never write for hardware changes, one boot check instead of two, the `boot_id` input goes, a write-only state file goes, six copies of the write code become one, and stale docs go. We add only what removal cannot do: two sentences that name the AIO cause, a hard-link copy of a settings file that cannot be loaded, and a re-check of a `degraded` storage verdict on a timer that already exists. Rough size: about −450/+250 production and doc lines, plus tests.

## 2. What we know and what we don't

**Known.** Version 0.2.0. The server is shut down nightly to copy a backup, then started. "Settings keep resetting." An earlier, separate problem in the same thread was that non-admins saw no shared files.

**Unknown.** Which setting changes. Whether Nextcloud is AIO. Whether the nightly routine only stops the host, or also recreates containers or restores files. No logs. 0.2.0 has no support bundle (bundles shipped in 0.3.0), so nothing below gives evidence from this install until it upgrades. Upgrading is not a fix: no change after 0.2.0 addresses a reboot reset, and upgrading from 0.2.0 starts a one-time model download.

**We are not asking the user for more now.** The change set below does not depend on the answers: every slice helps whichever cause is the real one. If the user writes again, these facts would settle it, in order of value:

1. What exactly looks reset after a reboot: the transcription switch, the model, "Connect Talk" needed again, Cassini missing from the Nextcloud menu, or a banner.
2. Whether Nextcloud is AIO, and if so whether `NEXTCLOUD_KEEP_DISABLED_APPS` is set and AIO's Talk Recording-server is on.
3. Whether the nightly routine only stops the host, or also recreates containers or restores files.

If they mention AIO with default options, the fix needs no release: set `NEXTCLOUD_KEEP_DISABLED_APPS=true`, turn off AIO's Talk Recording-server, then press Connect Talk once. The same flag also stops AIO removing other apps the admin disables later.

## 3. Ranked likely causes

| # | Cause | Confidence | Evidence | Addressed by |
|---|---|---|---|---|
| 1 | AIO deletes or rewrites Talk's `recording_servers` on every Nextcloud start. With default settings it can also disable AppAPI, which makes Cassini vanish from Nextcloud. Cassini's files stay intact. The product never names this cause. | Medium (only if AIO) | AIO entrypoint (upstream); `cassini-go-recorder/internal/talk/readiness.go:60-61` gives one generic message for 401/403; Connect Talk text `cassini-viewer/src/i18n/en/app/operator.ts:638-641` has no AIO line (removed in 0c5ae24f, before v0.2.0) | A1, A2, A4; support reply |
| 2 | Every host reboot (not a container restart) marks all models "installed but not ready". Tiers are greyed until a probe finishes, a tier change gets 409, and a model load competes with Nextcloud's start. | Medium | `transcribe/model_store.go:178` hashes `/proc/sys/kernel/random/boot_id`; `modelstore/readiness.go:21-31` needs an exact match; `settings.go:917-918`; `model_migration.go:172-205` | B1 |
| 3 | The boot storage check runs before Nextcloud answers. A 5xx or 429 is read as "account does not exist" or "sharing off", which refuses recordings. Otherwise the verdict is `degraded` and a banner tells everyone setup "needs an administrator" until a publish or a click. The banner advises "disable and enable the app". | Medium-low | `nc_provision.go:94-96`; `nc_direct_preflight.go:50-52`; `nc_access_status.go:139-149`; `run.go:493` + `run.go:543-546` (two runs); `cassini-app/src/operator/setupHealth.ts:226-229` | B2–B6 |
| 4 | With an automatic quality policy, a hardware fingerprint change at boot (GPU visible late, CPU count) rewrites `settings.json` and drops the device override. If that write fails, the whole parsed policy is discarded for the stand-in. | Low-medium | `settings.go:399-418` (whitelist omits `DeviceOverride`; Save error at `:414`); `run.go:952-955` | C2 |
| 5 | Cassini state really is missing or new: no volume plus a recreate, tmpfs, or a restore of `jobs.sqlite3` without the sidecars. Defaults are recreated silently and a new Talk secret is generated. | Low for default AppAPI | `settings.go:335-341`; `llm_settings.go:234-239`; `talk_provisioning.go:93-115`; `exapp.go:787-837` | A4, C3, D2, D4 |
| 6 | A torn or rejected `settings.json` puts Cassini on the stand-in; the next admin save overwrites the only copy. | Low (needs a crash or a restored crash-consistent copy) | `run.go:953`; `settings.go:935`; `llm_settings.go:829` | C4, E1 |
| 7 | The boot Talk probe forgets the saved test room after a 404 and persists that. | Low | `recording_readiness.go:640-666`; `talk_test_room.go:103-110` | Decision O3 |
| 8 | At runtime, opening Settings while the file is missing writes defaults and swaps them into memory. | Low | `settings.go:787`; `llm_settings.go:759` | C1 |

## 4. The change set

Slices are ordered by value and risk. Each slice is one PR unless noted. Every critic adjustment from round 2 is applied. Line counts are rough.

### Slice A — Name the AIO cause (wording and docs only, P0)

Docs-only parts (A3, A4) can merge now and need no changelog fragment. Code wording (A1, A2, A5) needs a `changelog.d` fragment, and CI requires the regenerated `cassini-viewer/src/i18n/i18n-types.ts`, because it carries the English text as JSDoc (`ci.yml:220-222`). Run `npm run i18n:sync -w cassini-viewer` and `npm run i18n:generate -w cassini-viewer`.

**A1. WORDING — One AIO sentence in the Connect Talk panel.** (+1/−1, plus 3 generated lines)
- Change: append to `backend.intro` (`operator.ts:640`): "On Nextcloud All-in-One, also turn off AIO's Talk Recording-server and set NEXTCLOUD_KEEP_DISABLED_APPS=true, or AIO removes these values every time Nextcloud starts. That setting also stops AIO removing other apps you disable later; see AIO restart persistence in Cassini's install guide."
- Helps: AIO admins, at the moment they redo the handoff.
- No one is worse off because the sentence is scoped to AIO, adds no key or component, and names the flag's side effect. The 10 other locales keep the old, still-true intro until Weblate updates them.
- Tests: none change; run the full cassini-app unit suite against the *edited* viewer (see process traps in section 7).

**A2. WORDING — Name the restart cause in the `recording_auth_rejected` message.** (+6/−6 across 5 files)
- Change: append, word for word identical in `readiness.go:61`, `cassini-viewer/src/i18n/en/app/doctor_checks.ts:57`, `cassini-app/src/preview/doctorScenarios.ts:232` and the regenerated files: "If this comes back after every Nextcloud restart, update or backup, something in Nextcloud's startup is resetting Talk's recording backend. Nextcloud All-in-One does this by default."
- Helps: the admin sees the cause on the row that fails each morning.
- No one is worse off because codes, actions and admission are unchanged and the sentence is a hedged conditional. `doctorScenarios.test.ts:161` enforces that the three copies match. (Minimal alternative if the owner wants fewer files: ship A1 only; the row's action already opens that panel.)
- Tests: `go test ./internal/talk`; operator `TestDoctorCatalog`; cassini-app unit suite; `i18n-sync --check`.

**A3. REMOVE — Docs for the removed Connect Talk command generator and backup.** (about −65/+6)
- Change: in `docs/recording-readiness.md`: shorten step 4 (`:84-89`) to "Open Connect Talk. Back up Talk's current recording_servers, then give Talk the two values shown (install guide, Step 5)", and drop the non-existent "administrator request". Delete `:180-181` and the "Environment-aware repair guidance" section (`:263-297`); keep the pointer at `:298-302` and the network-findings paragraph. Replace `:201-215` with one link to the install guide's Rollback block. Move the `call_recording` undo note into that block (`docs/exapp-install.md:612-620`). Change `:139-140` and `:309-310` to "re-checked once when the ExApp starts". In `docs/exapp-install.md:575-576` drop "Connect Talk generates these commands". In `docs/runbooks/validate-aio-restart-persistence.md:39` name `handoff_talk_recording` instead of a line number.
- Helps: admins stop looking for a warning and a backup file that do not exist.
- No one is worse off because no anchor in use is removed (`#aio-restart-persistence` and `#harp-routing-and-missing-navigation` stay), and the `call_recording` rollback note moves rather than disappears.
- Tests: none.

**A4. WORDING/DOCS — One storage-and-backup block, one AIO sentence.** (about −8/+16 per page; `docs/exapp-install.md` and the microsite `getting-started/install.md`)
- Change, "Persistent storage" (`docs/exapp-install.md:709-741`): replace the file list with "the whole AppAPI volume (`nc_app_<appid>_data`) is one unit: job database, settings, AI provider keys, the Talk recording secret, models and recordings." Then short sentences: back up and restore the whole volume together; restoring only `jobs.sqlite3` brings back history but not settings, and gives a new Talk secret (press Connect Talk again). Stop the Cassini container, or shut the server down, when nothing is recording or processing. The backup holds credentials; protect it. Check whether your backup tool includes the AppAPI volume. Talk's `recording_servers` is stored in Nextcloud, not on this volume. `talk-provisioning.json` exists only when `CASSINI_TALK_RECORDING_SECRET` is not set. Container-layer (overlay) data survives a container restart, and a host reboot only when the same container is started again; it is lost when the container is recreated (`docker compose down/up`, `docker run --rm`, including a boot script that does this, an app update or reinstall). tmpfs loses its data at every stop or reboot. The image has no `VOLUME` line on purpose. Outside AppAPI: back up the directory holding `jobs.sqlite3` plus any work, model or template directory you configured elsewhere.
- Sentences that land with later slices: "settings.json is created by the first save in Settings; a hand edit of settings.json or llm-settings.json needs a restart; a `*.rejected-N` file is a copy of a file Cassini could not load (move it back and restart; delete the LLM copy when done, it holds API keys)" (slice C); removing `app-state.json` from any list (E2).
- AIO paragraph (`:117-122`): one sentence: "Set NEXTCLOUD_KEEP_DISABLED_APPS=true and turn off AIO's Talk Recording-server; otherwise AIO's current startup script resets Talk's recording backend, and can disable AppAPI, on every start. See AIO restart persistence." In `recording-readiness.md:196-199`, state the full upstream condition: AppAPI is disabled only when `REMOVE_DISABLED_APPS=yes` and neither AIO's HaRP nor Docker Socket Proxy is on. Tell no one to re-register a working daemon.
- Helps: AIO, docker and bare-AppAPI admins, and support.
- No one is worse off because it replaces a list that had already drifted (it names `app-state.json` and omits 9 entries) and keeps `site/` inside the unit. Text is generic; no hosts.

**A5. WORDING — Base the "Cassini does not write recording_servers" comment on policy.** (+2/−2)
- Change: in `talk_provisioning.go:27-30` replace "has no OCS API an ExApp may call" with "Cassini deliberately does not write it: Talk's recorder choice belongs to the admin, another recorder may be in use, and AIO rewrites it at startup." Keep the AppAPI app-id scoping fact and the pointers to the install guide and `sandbox/wire-cassini.sh` (which reads the `recording_secret` key). Ship it with A1/A2.
- No one is worse off: comment only. It discourages a future auto re-apply that would fight AIO and other recorders.

### Slice B — A host reboot is not a reset (P1)

**B1. REMOVE — `boot_id` from the model-runtime fingerprint.** (−6/+4, plus 3 comments)
- Change: `transcribe/model_store.go:176-182` reads only `/proc/driver/nvidia/version`; comment "A GPU driver or native library replacement rechecks locally too; a reboot alone does not." Fix comments made false: `settings.go:903-904` ("every upgrade or native-runtime change"), `cassini-go-recorder/internal/cassini/models.go:36-38` ("unlike a receipt, survives an upgrade"), `docs/proposals/optional-transcription-model-storage/implementation.md:26`.
- Helps: every host that reboots. Tiers stay selectable, no spurious 409, no model load at boot (which also removes one GPU-memory spike at a busy moment).
- No one is worse off because the recheck never re-hashed weights (`modelstore/store.go:79-108`); upgrades still recheck once (the executable's size and mtime change); a driver change still rechecks. A GPU that breaks across a reboot with the same driver version is now caught by the build, whose existing fallback keeps the audio (`build_runtime.go:805-830`). The CI offline-recheck step forces a recheck with `touch` (`harness/bin/ci-transcribe-smoke-exapp.sh:182`), so it still works.
- Tests: existing transcribe/modelstore/cassini packages pass; optional: fingerprint stable across two calls.

**B2. SIMPLIFY — `unavailable` only for a definite Nextcloud answer.** (−6/+18)
- Change: `userExists` (`nc_provision.go:94`) returns an error for status ≥500 or 429. `ensureServiceAccountOnEnable` (`nc_owner_account.go:19-27`) returns an error when the confirming lookup errors; include the create refusal text in it when there is one. `nc_direct_preflight.go:26` records that as `degraded("nextcloud_probe")`. `nc_direct_preflight.go:52`: `unavailable` only for an `*ncShareError` below 500, else `degraded`, same step name `sharing_api`. Edit the `cassini-microsite/src/content/docs/guides/troubleshooting.md:89` row to key on `step=sharing_api` and cover both states.
- Helps: recordings are no longer refused because Nextcloud was busy for a moment; admins get the true cause instead of "account does not exist" plus `occ user:add`.
- No one is worse off because definite refusals (account confirmed missing, Share API off as a JSON OCS 404) keep their state, text and remedy. Trade-off for the release note: a share API that fails with 5xx for a long time now lets a call record; publish then fails with the local media kept (`media_cleanup.go:21-23`) and can be rerun. This matches how `degraded` already works for other steps.
- Tests: prototyped table test (lookup 503/429, 404+create 403/500, share 502/503 HTML, 503 OCS, 429, deadline, Share API off).

**B3. REMOVE — The duplicate, untracked boot preflight.** (−6/+11)
- Change: delete `run.go:493`. Replace `run.go:541-546` with one tracked goroutine for active ExApps: run `preflightDirectShares` with the configured config, then the readiness check with `scope.storage = false`. Rewrite the comment that cites the non-existent `preflightOnRestart`.
- Helps: half the boot calls to Nextcloud; no expired second run overwriting a good verdict; Talk probes get a fresh 25 s budget.
- No one is worse off because the set of installs that get a boot check is unchanged (gated on `exappCfg.Active`), storage still runs before the Talk probe (which acts as the admin that storage resolves, `talk_test_room.go:197-199`), and the support-journal config is kept. PR note: the panel's "Check again" storage branch still uses a fresh config with a 25 s budget (unchanged).
- Tests: full operator suite; `-race` on Run/Readiness/Preflight/Storage.

**B4. WORDING — Setup notice: drop the "disable and enable the app" step and a dead branch.** (−9/+3, plus key removal)
- Change: in `setupHealth.ts` delete `rerunSetup()` and the never-emitted `"administrator"` branch. Reword `shell.ts:78-79`: "You can still browse recordings. New recordings may not be saved until this is fixed." / "You can still browse recordings. Cassini checks again before it saves a new recording." Remove the two unused keys from `en` and all locale JSONs in the same PR. Update the comments at `status.go:103,114`. In troubleshooting change only `:88` to "then press Try again"; keep `:92` (restart is the right remedy there).
- Helps: removes the advice that looks most like "reset the app"; stops telling non-admins their recordings "will fail" when they will not.
- No one is worse off because "Try again" on the same notice runs the same check as the enabled edge (`storage_handler.go:46-49` vs `run.go:488-492`) and always renders.
- Tests: regenerate `i18n-types.ts`; run cassini-app vitest.

**B5. REMOVE — Duplicate `format=json`** at `nc_admin_identity.go:158` (the helper already adds it). ±1. No visible change.

**B6. ADD (justified) — Re-check a boot-race storage verdict on the existing 5-minute tick.** (+22) — needs decision O1
- Change: split `preflightDirectShares` into Lock + `preflightDirectSharesLocked`. Add `recheckDegradedStorage`: returns unless the app is AppAPI-active, `provisionMu.TryLock()` succeeds, and the verdict is `degraded` with step `nextcloud_probe` (the boot-race signature). Call it from the storage-usage timer (`storage_usage_artifacts.go:110-128`), after `refreshDetailedStorageUsage`, with a 90 s timeout. Update the `run.go:529-530` comment and `docs/recording-readiness.md:136-139`.
- Why removal cannot do it: the stale verdict exists because the only check ran before Nextcloud was up. Deleting the boot check brings back D-669 (state stuck at `unknown`). Probing on GET is refused by design. So something must look again; this reuses a loop that already calls Nextcloud on that cadence and adds no goroutine, file or setting.
- Helps: the reboot banner clears within about 5 minutes of Nextcloud answering, with no click.
- No one is worse off because a healthy install makes zero extra requests, TryLock never queues behind or delays Shutdown, and the step gate keeps real faults (share API 5xx, private archive) off the timer, so the support journal does not fill with `share_list` events. Talk rows are not refreshed (verified: with storage passing, stale Talk warn rows give `warn`, not the banner).
- Tests: prototyped `TestRecheckDegradedStorage` (recovers; skips when the lock is held; zero requests when provisioned or unavailable); add a case for a non-`nextcloud_probe` step.

### Slice C — Settings: read once at start, write only on save (P1, one PR)

The files are read once, in `NewRuntime`. After that, memory is the truth, and only PUT and the retired-model migration write, both updating memory too (`settings.go:935/939`, `model_migration.go:156/159`, `llm_settings.go:829/833`). C2 must land with or before C4, and with C1 (otherwise a write failure on a healthy file would create misleading copies, and a later PUT could save the stand-in over an intact file).

**C1. REMOVE — GET `/settings` and GET `/settings/llm` become pure in-memory reads.** (−27/+6)
- Change: `handleGetSettings` (`settings.go:782-797`) → `s := rt.currentSettings(); writeJSON(…settingsResponse{s, rt.effectiveFor(s)})`. `handleGetLLMSettings` (`llm_settings.go:755-766`) → `writeJSON(w, 200, rt.currentLLMSettings().view())`. Comment: "A read never writes: the file is loaded once at start and replaced only by a save." Keep `settingsWriteMu` (PUT vs migration).
- Helps: opening Settings can no longer write defaults, drop providers and keys, or race a concurrent LLM save (claim 1e disappears).
- No one is worse off because no peer process writes these files (checked in this repo) and the response is what jobs use. Stated in the PR: a hand edit now needs a restart (as retention already says, `retention_settings.go:338`); a transient boot read error is now retried by a restart, not a GET (decision O6); a file deleted at runtime is still gone at the next restart; under an automatic policy a GPU that appears after start is picked up at the next restart.
- Tests: move `TestGetSettingsLogsPersistedPolicyMigration` (`settings_test.go:503`) to the start-up loader; add "delete/truncate both files after PUT; GET returns the running policy and recreates nothing".

**C2. SIMPLIFY — Loading never writes for a hardware change; a failed write never discards a parsed policy.** (about −50/+15; merges three round-2 items)
- Change: replace `settings.go:394` to the end of the loader with: refresh `DetectedGPU`, `Cores`, `HardwareFingerprint` in memory; if `Source == auto`, set `Quality = defaultQualityForHardware(cudaCapableHost())` in memory (today's formula, not an override-aware one) and keep `DeviceOverride`, except that a `cuda` pin the host cannot serve is cleared in memory only (decision O2). The only write left is the existing D-684 clearing of an unsupported override (only a hand edit produces one now; PUT validates at `settings.go:870-879`). It is best-effort: on success it is reported; on failure the error text goes into the migration message and the next start retries. Return `s, nil` after a parse. Delete the copy-back whitelist, the fingerprint comparison, `needsRewrite` and the display-only Save. Update the doc comments at `settings.go:234-236` and `:318-326`.
- Helps: an intact policy is never demoted to the stand-in because a write failed at boot; GPU or CPU flaps no longer rewrite the file or drop a `cpu` pin; a future field cannot be lost by being missing from the whitelist.
- No one is worse off because the stored automatic tier already equals the live derivation for every upgraded install (7d2de948 changed formula and fingerprint together), nothing reads the persisted hardware fields from disk (`settings.go:400` was the only reader), and the UI reads them from memory. The round-2 override-aware formula was rejected by its critic: it would flip a stored `best` to `balanced` for an auto + `cpu` pin + fp32 setup and cause 409s.
- Tests: change the `TestCaptureOnlyUpdatePreservesQualityPolicy` fixture (`capture_policy_test.go:216-235`). Add: auto + `cpu` + stale fingerprint keeps `cpu` and leaves file bytes unchanged; auto + `cpu` on a CUDA-capable host (stub `probeNVIDIADevice`, as `status_test.go:398`) keeps `best`; auto + `cuda` on a host without CUDA clears in memory only; read-only dir + stale fingerprint loads and keeps transcription, audience and retention (skip when root); user-source file loads with live hardware fields and unchanged bytes. Tests that use the loader as a disk probe should compare bytes.

**C3. REMOVE — `settings.json` is created by the first save, not by loading a missing file.** (−5/+3)
- Change: the NotExist branch (`settings.go:335-341`) returns `detectSettings(), nil`. Fix `run.go:949` ("Detect hardware on first start"). `llm-settings.json` keeps its first-start seed write, because a test pins the env-freeze behaviour (`llm_settings_test.go:226-251`).
- Helps: support. Absence of `settings.json` beside job history now stays visible across restarts. It means "never saved here, or lost", not proof of loss.
- No one is worse off because an absent file gives the same in-memory defaults at every start, PUT creates the file (`settings.go:493`), and the model migration returns early with no active model (`model_migration.go:113-115`).
- Tests: rename `TestLoadOrInitSettingsFirstStartWritesAutoDefault` (`settings_test.go:165`) and assert the file is not created.

**C4. ADD (justified) — Keep a hard-link copy of a settings file that cannot be loaded.** (+28 new file, 2 log lines reworded)
- Change: `keepRejectedCopy(path)`: `os.Stat`, then `os.Link(path, path+".rejected-"+mtimeUnix)`, tolerating "exists". Call it only in the two failure branches in `NewRuntime` (`run.go:953, 961`). New wording: "stt_settings could not be loaded (%v); running on defaults (transcription off). The file is kept as settings.json.rejected-N. Restart to retry, or move that file back over settings.json and restart. Saving Settings now replaces settings.json and does not restore it." Same for LLM ("using the deploy environment"). When no copy is made (fresh install, link unsupported), the copy sentence is omitted. Add a one-line comment on the atomic writer: saves must stay write-temp-then-rename, because this copy relies on it.
- Why removal cannot do it: after a failed load the first save replaces the only copy of the policy, providers and keys. Refusing the save locks out admins without volume access; a recovery UI is far more code. A hard link copies no data, keeps the file's 0600 mode, survives the next atomic rename, and the mtime name makes it idempotent across nightly restarts.
- No one is worse off because it never runs on a healthy or missing file; nothing is gated, so Settings stay editable (the round-1 fresh-install lockout cannot happen); link failure gives today's behaviour; support bundles do not collect the copy. The word "rejected" is used because a readable file can fail validation.
- Tests: prototyped (one copy with the damaged bytes, 0600 kept, no second copy on restart, PUT 200; fresh install with a bad LLM env makes no copy and is not locked out).

**C5. WORDING — Comments and docs made false by C1–C4.** (about ±10) Rewrite the loader and GET comments; wrap the LLM seed-save error as "the AI endpoint from the deploy environment cannot be saved: %w"; the doc lines are in A4. Do not do the 45-site `LoadOrInitSettings` rename (merge churn, no behaviour gain).

### Slice D — Evidence survives the next reboot (P2)

**D1. REORDER — Start the support journal right after the work-root lock.** (+12/−9 in `run.go`, ±5 test call sites)
- Change: in `Run`, move `lockWorkRoot` up to just after config loading, then create the journal and the tee'd logger, then `resolveTalkProvisioning`, `OpenStore`, `MarkIncompleteJobsInterrupted`. Pass the journal into `NewRuntime` and delete its own creation (`run.go:877`). `requestLog` stays stderr-only (comment). The two fatal exits after the journal (`run.go:418-428`) use `logger.Printf`.
- Helps: the Talk secret generate/fail lines and the interrupted-jobs line reach bundles; a second operator that fails the lock no longer writes a secret first.
- No one is worse off because stderr text and files written are the same, and `talk-provisioning.json` is still written before listen (`sandbox/wire-cassini.sh:276-283`). The test sites pass real journals under their own work roots, not `nil`, so fixtures match production; `TestSupportJournalExcludesRequestLines` guards the request log.
- Tests: full suite; prototyped Run-level test over four boots.

**D2. ADD (justified) — Log when a settings file is absent at start.** (+8 in `run.go`; needs C1 and C3)
- Change: before each loader, `existed := fileExists(path)` (`recorded_at.go:306`). On success with `!existed`: "settings.json was not found; using detected defaults (transcription off) until an administrator saves Settings" and "llm_settings created from the deploy environment (llm-settings.json was not found)". Base names only.
- Why removal cannot do it: nothing records the event today. After C1 and C3, boot is the only place these states arise, so two lines cover every case.
- No one is worse off because 0.2.x–0.4.0 installs already have `llm-settings.json` and normally `settings.json`; the line is info, not a warning, and true on a fresh install and after a loss.

**D3. WORDING — Say what to do after a new Talk secret.** (±1) Keep the prefix at `talk_provisioning.go:178`; replace the tail with "; if Talk already used Cassini for recording, Talk still holds the old secret: press Connect Talk again." Drop the sandbox script name from this production line.

**D4. SIMPLIFY — One ephemeral-storage check: `statfs` in Go; delete the shell copy and its path mirror.** (about −70/+20)
- Change: replace the `/proc/mounts` parser (`exapp.go:802-837`) with `syscall.Statfs`, comparing `int64(st.Type)` with overlay `0x794c7630` and tmpfs `0x01021994`. Branch the wording: overlay "… is on the container layer (overlay): it is lost when the container is recreated or the app is updated; mount a persistent volume"; tmpfs "… is on tmpfs: it is lost at every container stop and host reboot; mount a persistent volume". Check `dir(DBPath)`, `WorkRoot` and `SiteRoot` (`run.go:516`). Delete `deployment/exapp-start.sh:114-139` and the "keep in sync" mirror comment (`exapp.go:150-154`). Add one scrub-safe boolean `data_on_ephemeral_storage` to `configuration.json` (`support_bundle.go:543`), computed from the DB dir and WorkRoot, plus SiteRoot only when the publish sink is local; set it before listen.
- Helps: removes the prefix, escape and symlink bugs (claim 4a) by deletion; one warning instead of two; correct words for a "reset at reboot" report (tmpfs really does reset at reboot).
- No one is worse off because a named volume reports its own filesystem (no warning, `false`), every checked path is already touched before listen (no new blocking), the operator creates the DB and work dirs itself, and nothing asserts on the shell output. Residual: a separately overridden site root on overlay is not warned about on the first boot of a local-sink install.
- Tests: the returned bool equals a `statfs` of the same path (do not assume `t.TempDir()` type); the key is present in a scrubbed bundle; darwin and arm builds.

### Slice E — Hygiene that removes failure modes (P2/P3)

**E1. SIMPLIFY — One atomic writer for every JSON sidecar.** (about −82/+18)
- Change: add a best-effort directory sync after the rename in `writeFileAtomic` (`util.go:11-32`), then use it in `Save`, `SaveLLMSettings`, `talkSecretStore.save`, `saveEventWebhookSettings`, `saveRecordingSetupLocked` and `retentionConfig.save`. Callers keep their own `MkdirAll` (Talk needs it, it runs before `OpenStore`). Fix `llm_settings_test.go:383` to glob the new temp name.
- Helps: a leftover `<name>.tmp` can no longer block saves; files survive power loss better; a retention save no longer answers 500 for a change already on disk (`retention_settings.go:321-327`).
- No one is worse off because names and modes are unchanged (`settings.json` is now chmod'ed to 0644 explicitly, which equals today under umask 022), and a refused directory sync is ignored. PR note: the directory sync also applies to the existing callers (site catalog, site bundle, meeting bundle, templates).

**E2. REMOVE — `app-state.json` and the lifecycle store.** (about −115/+6, tests −75/+12)
- Change: delete the store, its types and both `Store.Update` 500 branches (`lifecycle.go:28-110, 186-190, 212-215`); `/enabled` and `/init` keep their replies, logs and callbacks. Drop `stateDir` from `installRoutes` (`exapp.go:295-299`, `run.go:1177`). Leave existing files alone.
- Helps: AppAPI enable and init can no longer fail with 500 because of a torn file or a full volume.
- No one is worse off because nothing reads the file (only tests call `Load()`), and a downgrade ignores it as before. Release note: the file is no longer written and can be deleted; external backup scripts should treat it as optional.

**E3. SIMPLIFY — One CLI model-root helper.** (−24/+18)
- Change: export `transcribe.DefaultCacheDir`: AppAPI redirect (trimmed `APP_PERSISTENT_STORAGE`, `CASSINI_CACHE_ROOT` empty or equal to the baked default → `filepath.Join(persist, "operator", "models")`), then `CASSINI_CACHE_ROOT`, then `os.UserHomeDir()`, then `user.Current()`, then `./.cache/cassini`. Delete `defaultCassiniCacheRoot` (`doctor.go:378-391`) and use the helper at its four call sites. Update `implementation.md:22`.
- Helps: `docker exec … cassini models import` lands where the operator reads, on the volume.
- No one is worse off because `cassini build` resolves exactly as today (HOME first), operator children always get an explicit root, and harness scripts pass `--cache-root` or run without `APP_PERSISTENT_STORAGE`. Not reboot-related; lowest priority.

## 5. Explicitly not doing

- `operator_state` table, `boot_count`, `installed_at`, per-sidecar `initialized_at`: a migration on a fatal startup path; false alarms on healthy upgrades, because several sidecars exist only after a first save; lost with the volume anyway.
- Health warning "jobs exist but a sidecar is missing" / `boot_count == 1` with remote recordings: the same false alarms.
- `created_at` on `talkProvisioningState`, secret fingerprints, `recording_secret_source` / `*_this_start` flags: per-process or heuristic; the journal line (D1, D3) records the fact durably.
- `storage.json`, `state_files ->` boot line, `storageClass` enums, `paths_under_persistent_storage`: replaced by D2, D4 and C3's durable absence.
- `settingsLoadErr` flags, GET 503, PUT 409 + `replace_unreadable`, set-aside with fallbacks, recovery UI in 11 locales: lock-out risk and much code; C4 makes the never-refused save harmless.
- Typed `settingsPersistError` / `storeUnreadableError`: after C2 a load error can only mean the file itself is bad.
- GET that stats, restores from memory and logs; `templatesMu` in the LLM GET: C1 removes the write instead.
- Bounded `convergeAfterBoot` retry loop, repeated Talk refresh, "will check again" banner string, `boot_convergence` enum: B2, B3 and B6 cover it with less.
- Probing on GET `/health`, `/status`, `/setup`, or at Talk start: refused by design (`run.go:529-530`, the AppAPI GET cache), and `/setup` is called by every user.
- Removing the boot check or the `unavailable` state entirely: brings back D-669, or records meetings that can only fail at publish.
- Replacing `boot_id` with kernel release, CPU flags or GPU UUIDs: just delete it (B1).
- Health row for persistence; `exapp-start.sh` cache-root check; `resolveConfigPath` for cache and templates roots; a `templates_root` log line: developer-only effect or covered by D4.
- Self-healing `app-state.json`: the file goes (E2). Startup sweeper for orphan temp files: microsecond window, harmless leftovers.
- `syncDirFn` test seam, keeping old error prefixes: no consumer.
- Empty `recording_secret` treated as an error: it would refuse Talk recording and need manual repair.
- Moving the Talk secret elsewhere (another file or Nextcloud app config): same durability or a new boot dependency, and breaks `sandbox/wire-cassini.sh`.
- Auto re-applying `recording_servers` from Cassini: unverified write access, fights AIO's own setting, overrides operators' chosen recorders.
- Logging rejected inbound Talk requests: in the AIO case Talk sends nothing; the outbound probe already catches a mismatch.
- Moving all sidecars into SQLite, or merging them into one `config.json`: large migration, different modes and locks, script and doc churn; does not address the report.
- `VOLUME` line in the Dockerfiles: an anonymous volume does not survive a recreate and is orphaned on every AppAPI update.
- Reason table for scrubbed bundles (`recording_secret_generated`, `settings_not_found`, …): bundles default to the original mode (`support_bundle.go:97-98`), which carries these lines after D1. Revisit only if a scrubbed bundle is the only evidence we get.
- Log line for an automatic quality that differs from the stored one: after C2 the effective quality and device are already in `configuration.json`.
- Telling the user "upgrade to 0.4.0 and it is fixed": false (see section 2).

## 6. Open decisions for the owner

Each decision says what happens today, what the choice is, and what we recommend. Only O1 and O2 block a slice (B6 and C2). The others can be decided later.

### O1. May Cassini look at Nextcloud again on its own after a boot? (blocks B6)

**Today.** When the container starts, Cassini checks once whether it can save recordings into Nextcloud. After a full server reboot, Cassini often starts before Nextcloud answers. That one check then fails and leaves a "degraded" result. A banner tells every user that setup needs an administrator. The banner stays until a recording is published (publication checks again) or an admin presses "Try again". Nothing else checks again, on purpose: the comment at `run.go:531-532` says the boot check is "NOT a timer". The reason for that rule was that probes on a timer, or probes started by opening a panel, cost Nextcloud requests and were refused in an earlier design.

**The choice.** Cassini already has a timer that calls Nextcloud every 5 minutes to measure storage use. B6 adds one step to that timer: if the last storage result is "degraded" *because Nextcloud did not answer*, check storage again. In every other state it does nothing.

- **Yes (recommended):** the morning banner clears by itself within about 5 minutes of Nextcloud coming up. A healthy install makes no extra requests. Real faults are not retried. We update the "NOT a timer" comment to say that only this one narrow re-check runs on the existing timer.
- **No:** B2–B4 still stop wrongly refused recordings, but after each reboot the banner stays until the first publish or a click. The cost is one confusing banner each morning, which an admin could read as "Cassini reset itself".

### O2. A pinned GPU that is not there at boot (blocks C2)

**Background.** Settings can pin the transcription device: `cpu` or `cuda` (GPU). Quality can be automatic, which means Cassini picks it from the hardware.

**Today.** With automatic quality, any hardware change at boot drops the device pin and saves that to disk. A hardware change includes a GPU that shows up a few seconds late, or a different CPU count. So an admin who pinned `cpu` (for example, to keep the GPU free for other work) can find Cassini back on the GPU after a reboot. C2 stops this: the pin is kept.

**The choice.** One case is left. The pin is `cuda`, but at boot Cassini cannot see a usable GPU, for example because the driver is late or the card is gone. What should happen?

- **Ignore the pin for this boot only, in memory (recommended).** Transcription runs on the CPU for now. The file still says `cuda`, so the next boot with a working GPU uses it again. One edge case: if an admin saves any other setting during that boot, the save writes the cleared pin. That is still narrower than today, where every hardware change clears the pin at once.
- **Clear the pin and save it (today's behaviour for this case).** The pin is lost for good after one bad boot.
- **Keep the pin and wait for the GPU.** Transcription is put off until a GPU appears, which may be never. We do not recommend this.

### O3. The boot check forgets the Talk test room (small fix, no slice yet)

**Background.** To test recording, Cassini creates a private Talk conversation, the "test room", and saves its link in `recording-setup.json`.

**Today.** Each readiness check asks whether that room still exists. If Talk answers "not found" (HTTP 404), Cassini forgets the room and saves that, so the next test creates a new one. But Talk gives the same 404 when the Talk app itself is not enabled, not only when the room is gone (`cassini-go-recorder/internal/talk/readiness.go:62-63`, `roomIsGone` in `talk_test_room.go:103-110`). The check also runs once at every boot, with nobody watching. So if Talk is briefly disabled when Cassini boots (during a Nextcloud upgrade, or while AIO is switching apps), Cassini forgets a perfectly good test room. To the admin, the recording test looks reset and must be prepared again. A Talk that does not answer at all does *not* cause this; only a 404 does. This is a minor "reset at reboot", which is why the cause table rates it low.

**The choice.**

- **The boot check never forgets a room; only an admin's "Check again" may (recommended, about 3 lines).** A room that really is gone is still forgotten the next time an admin checks.
- **Forget only rooms whose owner Cassini knows.** This would leave rooms from older versions, which have no recorded owner, stuck forever when they really are gone.
- **Leave it as it is.**

### O4. One bad value rejects the whole settings file (rule, no code)

**Today.** When `settings.json` loads, every field is validated. Examples: at most N transcription terms, each term at most M characters, alias limits, retention rules (`settings.go:150-200`, `source_retention.go:33-43`). If *one* value breaks a limit, the whole file is rejected. Cassini then runs on the stand-in policy (transcription off). Today no saved file can break a limit, because saves are validated with the same limits.

**The risk.** A future release that *lowers* a limit would turn every install that saved more than the new limit into "transcription is off after the upgrade". That is a reset caused by us.

**The choice.**

- **A review rule, no code now (recommended).** Any PR that tightens a settings limit must, on load, trim or drop the offending value with a log line instead of rejecting the file. Write this rule in the code comment above the limits and in CONTRIBUTING.
- **Change the loader now** so that every optional field is trimmed instead of rejected. This is more code and changes error behaviour that has no current failure.

### O5. A shutdown while a meeting is processing (separate issue)

**Today.** When the container stops, a transcription ("build") that is running is cancelled. AppAPI gives the container only Docker's default 10 seconds to stop, so long builds never finish. The job ends as "failed" or "interrupted", and nothing runs it again by itself. If the admin chose "delete source media after processing", the cleanup worker treats that job as finished and deletes the recorded audio (`media_cleanup.go:21-23`). The meeting is then gone: no transcript and no audio. A nightly shutdown at the wrong moment can cause this.

**The choice.** It is not a settings reset, so it is not in this change set. The question is only whether to track it now.

- **Open a separate issue (recommended).** The likely fix is small: on shutdown, put the build back in the queue, the same way a build already steps aside for a higher-priority job, instead of failing it. No new marker or state is needed.
- **Fold it into this proposal.** That makes the proposal larger and touches job-state code, which carries more risk.

### O6. A settings file that could not be read at boot: how does it recover? (C1)

**Today.** If `settings.json` cannot be read at boot (a passing I/O error, for example), Cassini runs on the stand-in policy. But each time an admin opens Settings, the GET request reads the file again. If the file is fine by then, the real settings quietly come back.

**After C1.** A GET request only serves what is in memory and never touches the file. That removes the bugs listed under C1. It also removes this accidental self-healing.

- **A restart is the way to recover (recommended).** The C4 log line says so in words. A passing read error at boot is rare, and a restart is a normal admin action.
- **Remember that the boot load failed, and let a GET try again while that is true.** This keeps the self-healing but adds back a code path that writes memory from a read request.

### O7. While settings cannot be loaded, who sees new recordings?

**Today.** If `settings.json` cannot be loaded, the stand-in policy shares new recordings with the *whole Talk room* (`detectSettings`, `settings.go:308`). If the admin had chosen "attendees only" or "moderators only", recordings made during that time reach more people than the admin allowed. Elsewhere, Cassini already fails closed: an unknown saved audience is read as attendees (`normalizeSavedAudience`, `settings.go:295-301`, #480).

**The choice.**

- **Use "attendees" in the failure case only (recommended, one line in `NewRuntime`'s fallback branch, `run.go:954`).** Fresh installs keep "room" as their default, because `detectSettings()` does not change. The only effect is that a broken settings file never widens who sees a recording.
- **Leave it.** C4 already keeps a copy of the file, so the settings are not lost. But recordings shared too widely during the failure cannot be unshared by restoring the file.

(The first draft of this proposal recommended leaving O7 alone. The narrower fix above touches neither fresh installs nor `detectSettings`, so it is now the recommendation.)

## 7. Appendix

### Claim verdicts for the original assessment

| Claim | Verdict | Reason |
|---|---|---|
| 1a NewRuntime fallback | Confirmed | `run.go:952-965`: logs once, runs on `detectSettings()` / `SeedLLMSettings`. |
| 1b GET hides the failure | Confirmed | `settings.go:782-797`, `llm_settings.go:755-766` return 200 with the stand-in. GET also re-reads (self-heals a repaired file) and writes when the file is missing. |
| 1c UI error handling exists | Confirmed | `SettingsPanel.svelte:286-318`, `LLMSettingsPanel.svelte:104-118`; the backend 200 bypasses it. |
| 1d PUT overwrites | Confirmed | `settings.go:935`, `llm_settings.go:829` merge onto the stand-in with no backup. |
| 1e LLM GET without lock | Confirmed | Logic race with PUT under `templatesMu` (`llm_settings.go:783`). Removed by C1. |
| 1f Peer stores do not log | Partially | Retention cause is not logged but sweeps halt safely (`retention_worker.go:96-100`); recording-setup loads lazily and is surfaced in readiness (`recording_readiness.go:719-724`). |
| 2a Unsynced fixed-`.tmp` writes | Confirmed | Five writers (`settings.go:513-520` etc.). A 0-byte `app-state.json` reads as zero state; corrupt JSON gives 500 on `/enabled` and `/init`. Nothing reads the file. |
| 2b Durability impact | Partially | A clean shutdown loses nothing; risk is power loss or a restored crash-consistent snapshot within about 30 s of a write. |
| 2c Boot rewrite for display fields | Confirmed | `settings.go:447-452`; also runs on GET. Removed by C2. |
| 3a Silent defaults beside a DB | Confirmed | `settings.go:333-342`, `llm_settings.go:231-240`, no log. |
| 3b New Talk secret when file missing | Partially | Logged to stderr only (`talk_provisioning.go:178`, before the journal); the boot probe then reports `recording_auth_rejected`. |
| 3b Empty secret regenerates | Confirmed | `talk_provisioning.go:99-103`; a 0-byte file is *not* regenerated, it disables Talk recording with an error. |
| 3c Provisioning order | Confirmed | Nothing reads the Talk fields before `NewRuntime`; moving after the journal is safe only by creating the journal earlier (D1). |
| 3d AIO disambiguation | Partially | One code for both causes; a `created_at` heuristic is unreliable. AIO is the larger cause. |
| 3 fix risk: `operator_state` on upgrade | Confirmed | Optional sidecars are written only on first save, so healthy upgrades would be flagged. Dropped. |
| 4a Mount prefix bug | Confirmed | `exapp.go:825` bare `HasPrefix`, no unescape, no symlink resolution. Removed by D4. |
| 4b Coverage | Partially | WorkRoot skipped; SiteRoot is still read for legacy content, so not "unused". |
| 4c Relative cache/template roots | Confirmed | `run.go:742-745`; developer-only effect. Not acted on. |
| 4d No warning in `/status`, `/health` | Confirmed | Only the startup log line; D4 adds a bundle boolean, no health row. |
| 5a One-shot boot probes | Partially | Publish, recheck and enabled edge re-probe; AppAPI does not resend `/enabled` on a restart; the boot preflight runs twice. |
| 5b `userExists` 5xx as missing | Confirmed | `nc_provision.go:94-96`. Fixed by B2. |
| 5c Share errors as `unavailable` | Confirmed | `nc_direct_preflight.go:50-52`. Fixed by B2. |
| 5d `unavailable` refuses recording | Confirmed | `nc_access_status.go:139-149`; no publish means no re-probe. |
| 6a Pre-journal logs lost | Confirmed | `run.go:406, 430` vs `:877-879`. Fixed by D1. |
| 6b Scrubbed bundles drop plain lines | Partially | Mechanism correct; scrubbed is *not* the default (original is, `support_bundle.go:97-98`). |
| 6c No persistence facts in bundle | Confirmed | `support_bundle.go:499-551`. D4 adds one boolean. |

### Additional gaps found (not in the original assessment)

- AIO resets Talk's `recording_servers` on every Nextcloud start; with default settings it also disables AppAPI (cause 1; A1–A4).
- `boot_id` in the model fingerprint (cause 2; B1).
- Auto policy re-derive drops the device override; a failed write discards the policy (cause 4; C2).
- The boot preflight runs twice, once untracked (B3).
- The boot Talk probe forgets the test room on a 404 (O3).
- One invalid optional field rejects the whole file (O4).
- Shutdown-cancelled builds are failed and their media deleted under "delete after processing" (O5).
- Docs describe a Connect Talk command generator and backup removed before v0.2.0 (A3).
- Backup guidance covers one of four SQLite databases and none of the sidecars (A4).
- `app-state.json` is write-only (E2).
- The in-container CLI uses a different model root from the operator (E3).
- No change after 0.2.0 fixes a reboot reset.

### Process traps for implementers

- Any English UI string change must commit the regenerated `i18n-types.ts`, or CI fails.
- `git archive` scratch copies keep `node_modules` as a symlink into the real repo, so cassini-app tests there resolve the *unmodified* viewer. Relink before trusting a vitest pass.
- `TestRecordAttemptStepOutcomesClearsAFallbackTheRewriteDidNotHave` is flaky at HEAD (about 1 in 10), unrelated to these changes.
- `.go` or `.ts` changes need a `changelog.d` fragment (`changelog-check.yml:76-79`).
- Prototype diffs for slices B, C and D exist outside the repo; ask the author.
