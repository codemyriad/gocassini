# Runbook: prove Cassini survives an AIO restart

**Ticket:** [D-763](https://linear.app/code-myriad/issue/D-763) — the last
blocking item on PR #305.
**Question this answers:** after Nextcloud All-in-One restarts, does Cassini
still record — or does AIO quietly take its recording backend away?

This is a live test on a real AIO installation. It cannot be done from a pull
request, and CI cannot answer it: CI restarts an *installed ExApp* in a harness
that is not AIO, so it never exercises AIO's own startup script.

Budget about 40 minutes, most of it waiting for containers.

---

## 1. Why this needs testing at all

AIO manages Talk's recording settings independently of Cassini, from its
[startup script](https://github.com/nextcloud/all-in-one/blob/main/Containers/nextcloud/entrypoint.sh):

| AIO state | What its startup does to `spreed recording_servers` |
|---|---|
| `TALK_RECORDING_ENABLED=yes` | overwrites it with AIO's own recorder |
| recorder disabled **and** `REMOVE_DISABLED_APPS=yes` | **deletes** it |
| recorder disabled **and** keep-disabled-apps set | leaves it alone |

Only the third row is safe, and reaching it is a mastercontainer change — see
[AIO restart persistence](../recording-readiness.md#aio-restart-persistence)
for the configuration itself. **This runbook does not repeat that; it proves it.**

There is a second case, newer and not covered by the branch's own handling:
**AppAPI itself can end up disabled** across a restart. If that happens Cassini
is not merely misconfigured, it is unreachable — and every readiness check will
fail in a way that looks like a Cassini fault rather than an AIO one.

## 2. Before you start

**Do not run `sandbox/wire-cassini.sh` at any point during this procedure.**
It sets `spreed recording_servers` at line 307, which is precisely the value
under test. Running it after the restart repairs the damage and reports success,
which is the one outcome that teaches us nothing. If the test fails, that script
is the *remedy* — run it afterwards, deliberately, once the result is recorded.

You need:

- an AIO installation you may restart, with Cassini installed and recording
- shell access to the Docker host
- a Talk room you can start a call in, and a second participant or a second
  browser session — a call with one person may not produce a recording worth
  publishing
- the configuration from
  [AIO restart persistence](../recording-readiness.md#aio-restart-persistence)
  already applied, and the mastercontainer recreated so it took effect

Confirm the last point before spending time on the rest:

```bash
docker exec nextcloud-aio-nextcloud sh -c '
  test "$TALK_RECORDING_ENABLED" != yes && test "$REMOVE_DISABLED_APPS" != yes
' && echo "AIO is configured to leave the backend alone"
```

A non-zero exit means the restart **will** take the backend away, and you are
testing the wrong configuration. Fix that first.

## 3. Baseline — capture this before restarting

A post-restart value is only evidence if you know what it was before. Record all
five; you will compare them line by line.

```bash
# 1. The recording backend Talk is pointed at. THE value under test.
docker exec -u www-data nextcloud-aio-nextcloud \
  php occ config:app:get spreed recording_servers

# 2. AppAPI enabled? (the newer failure case)
docker exec -u www-data nextcloud-aio-nextcloud \
  php occ app:list | grep -A1 -i app_api

# 3. Cassini registered and enabled as an ExApp
docker exec -u www-data nextcloud-aio-nextcloud \
  php occ app_api:app:list

# 4. Talk's signaling secret is still configured (presence, NOT the value)
docker exec -u www-data nextcloud-aio-nextcloud \
  php occ config:app:get spreed signaling_servers > /dev/null \
  && echo "signaling configured"

# 5. Cassini's own view, which is the one a person sees
#    Operator › Publish pipeline › recording checks → all passed
```

Then **record and publish one meeting**, end to end, and confirm playback. That
is the baseline the post-restart test is compared against — without it, a
failure afterwards cannot be told from a deployment that never worked.

## 4. The restart

Use AIO's normal controls — the mastercontainer UI's stop/start, or a host
reboot if you are testing that. **Not** `docker restart` on individual
containers: that skips the startup script whose behaviour is the entire subject
of this test.

Wait for AIO to report every container healthy before continuing.

## 5. After the restart — check in this order

The order matters: each step's failure explains the next step's symptom, and
checking them out of order invites the wrong diagnosis.

| # | Check | Command | A failure here means |
|---|---|---|---|
| 1 | AppAPI still enabled | `occ app:list \| grep -A1 -i app_api` | Cassini is unreachable; every later check fails for this reason, not its own |
| 2 | Cassini still registered | `occ app_api:app:list` | the ExApp registration did not survive; readiness routes will 404 |
| 3 | Backend still Cassini | `occ config:app:get spreed recording_servers` | **the failure this runbook exists to find** — compare against the baseline |
| 4 | Signaling still configured | `occ config:app:get spreed signaling_servers` | Talk cannot reach the HPB; recording starts and never connects |
| 5 | Cassini agrees | Operator › Publish pipeline → **Check again** | the checks and reality disagree; note which, that is its own bug |

Container health is **not** one of these. Every container can be healthy while
the recording backend has been deleted out from under Cassini — that is the
normal shape of this failure, and it is why "the stack came up fine" is not an
answer.

## 6. The decisive test

Checks 1–5 can all pass and recording still be broken. Finish the job:

**Start a real call in Talk, record it, and follow it through to published
playback.** Not a synthetic job, not a readiness probe — the same path a user
takes. Confirm you can play the result back.

Only that proves the restart was survivable.

## 7. Recording the result

Put the outcome on D-763 either way. A pass is as valuable as a failure here,
because the whole point is to stop guessing.

**If it passed**, note: the AIO version, the keep-disabled-apps setting you
used, and that a real recording published after the restart. That closes the
acceptance item.

**If it failed**, note *which* of the five checks failed and its before/after
values. The distinction that matters most:

- **check 3 changed** → AIO's startup took the backend. The configuration in
  [AIO restart persistence](../recording-readiness.md#aio-restart-persistence)
  is insufficient or was not applied correctly; that guidance needs revising.
- **check 1 failed** → AppAPI disablement, which the branch does not yet
  address. This is a new finding and needs its own handling, not a docs fix.

Then, and only then, repair the instance:

```bash
# The rollback the branch's handoff offers, for a backend AIO removed.
# Substitute the exact backup filename printed during Connect Talk.
previous=$(cat ./cassini-recording-backend.XXXXXX)
docker exec -u www-data nextcloud-aio-nextcloud \
  php occ config:app:set spreed recording_servers --value="$previous"
```

or, on the sandbox, re-run `sandbox/wire-cassini.sh` — now that the result is
recorded and the script can no longer hide it.

## 8. What this does not cover

- **Non-AIO platforms.** The generated Docker/Compose/host/Snap commands are
  separately unvalidated; a Snap `occ` wrapper is not proof of ExApp
  compatibility. Same acceptance list, different runbook.
- **Repeated restarts.** One restart proves the startup script's behaviour under
  this configuration. It does not prove an AIO *upgrade* preserves it, which
  rewrites more than it starts.
