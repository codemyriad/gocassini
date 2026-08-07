# `ops/deploy` — how *we* deploy our own Cassini instance

This directory is **our operations tooling, not a supported product surface.**
It exists so that the deploy of `cloud.codemyriad.io` is reproducible from a
committed script instead of being reverse-engineered from the running
containers. It is not the path an end user takes and is not covered by any
compatibility promise — the flags, the inventory format and the layout here can
change without a deprecation cycle.

**If you are installing Cassini, you want [`docs/exapp-install.md`](../../docs/exapp-install.md).**
The supported user path is the Nextcloud admin UI: External Apps → Install. What
that path can and cannot deliver is written down in
[`docs/exapp-update-constraints.md`](../../docs/exapp-update-constraints.md).

## What is here

| Path | What it is |
|---|---|
| `deploy-exapp.sh` | The deploy. Dry-run by default; `--apply` to act. |
| `lib/exapp-register.sh` | The AppAPI register logic — **one implementation, two callers.** Also sourced by `harness/bin/lib/stack.sh`. (The AIO sandbox's `wire-cassini.sh` registers inline and does not source it.) |
| `test-exapp-register.sh` | Offline unit tests for the library's decision rules. Runs in the per-PR lint gate. |
| `inventory/` | Non-secret topology parameters, one file per deployment. |

`lib/exapp-register.sh` is the reason this lives in the product repo rather than
in our systems repo: the e2e harness sources it directly, and the rules it
encodes (never `--rm-data`, unregister before re-register, force
`PUT /enabled`) have to hold identically in both places or they will drift.

## Our real inventory is not in this repo

`inventory/` here contains **one illustrative example**. The inventory for
`cloud.codemyriad.io` lives in the private systems repo, under
`systems/codemyriad/cassini-exapp/deploy/`, together with the topology notes
that go with it — it describes our specific hosts, LAN and tailnet addressing,
container names, LXC ids, and where the deploy resolves each secret from. That
is our operations, not part of the product.

No credential values are stored in either place. An inventory records the
*command that resolves a secret on the target host*, so values never reach an
operator's machine.

## An inventory is executable shell, not config

`deploy-exapp.sh` **sources** the inventory, and `eval`s
`CASSINI_IMAGE_INSPECT_CMD` **on the machine you run the deploy from**. A
`CASSINI_SECRET_CMD_*` entry is likewise shell, executed on the target host.
Review an inventory the way you would review a script, and never run one you did
not write or read.

## The example is an example, not a supported topology

`inventory/example-cpu-local.env` exists to make one point concrete: a CPU
deploy and a GPU deploy are the *same* deploy with one different value. AppAPI
derives the image variant (`<image-tag>-cuda`) from the **daemon's** compute
device, so there is no GPU code path in this directory and there must never be
one.

It is not a statement that we support that topology. Which ExApp deployment
shapes we officially support — same-host CPU, same-host GPU, remote GPU over
HaRP + FRP — is still open (D-581), and the answer will shape both this script
and the one-click install work. Until then, treat the example as a worked
illustration of the mechanism.

## Usage

```sh
# Dry run — reads live state, changes nothing
ops/deploy/deploy-exapp.sh --inventory <inventory>.env --tag X.Y.Z

# Apply
ops/deploy/deploy-exapp.sh --inventory <inventory>.env --tag X.Y.Z --apply
```

Rollback is the same command with the previous tag; the script prints it on
success. Moving tags (`latest`, `latest-cuda`, `cuda`, …) are refused — they are
not rollback targets and the same registration resolves to different bytes
tomorrow. `--help` lists the rest.
