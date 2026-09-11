# Recording setup: community evidence and proposal comparison

Assessment date: 2026-09-11. Related: D-763, draft PR #289.

An agy agent researched the community archive in read-only mode. Its examples
were checked against the database and its numerical conclusions were audited
before preparing this assessment. The original report remains an unreviewed
research input, not a source of installation market shares.

## What the archive can tell us

A reproducible, deliberately narrow audit examined 6,034 opening posts from
4,213 authors in `help.nextcloud.com` threads created between 2025-01-01 and
2026-09-11. Of those, 2,030 had a bounded, nonempty installation-method field
that the parser could extract (1,666 authors). The parser excludes template
examples and unfilled placeholders. It does not classify whole posts from
incidental keywords or turn missing fields into an inferred installation type.

| Explicit field signal | Topics | Distinct authors within signal |
| --- | ---: | ---: |
| AIO / All-in-One | 570 | 498 |
| Docker / Compose / Portainer | 467 | 415 |
| Bare metal / archive / manual | 435 | 372 |
| NAS names | 84 | 74 |
| NextcloudPi / NCP | 54 | 48 |
| Snap | 53 | 49 |
| Kubernetes / Helm | 18 | 17 |
| Managed/shared/provider names | 31 | 29 |

These are overlapping signals, not mutually exclusive installation counts.
AIO can be deployed with Compose on a NAS; a Snap installation can also be
called bare metal. Provider names do not prove managed hosting or lack of SSH.
Authors can describe multiple installations. This support corpus is not a
representative survey; trouble-free users and provider customers may be absent.
The figures establish coverage needs, not audience percentages or skill levels.
They also undercount descriptions outside the support template.

Reproduce with `python3 audit.py /path/to/community-monitoring/data/archive.db`.
The date window is fixed in the script so future runs do not silently change it.

## Verified examples and their implications

1. **Host installation does not imply Docker competence.** In [How to setup
   AppAPI](https://help.nextcloud.com/t/how-to-setup-appapi/247991), 11–14 August
   2026, the author describes Rocky Linux/Apache, states that Nextcloud runs
   directly on the system and Docker is installed, then says they are unfamiliar
   with Docker. They need help understanding the connection URL and certificates.
   The agent's claim that this user intentionally avoided Docker is contradicted
   by the opening post. Our current command handoff still assumes too much.
2. **Other Docker installations have distinct service layouts.** A [9 August
   2026 recording report](https://help.nextcloud.com/t/247927) describes LinuxServer
   Nextcloud, a separate HPB container, a separate recording backend and Traefik
   subdomains. The reported failures concern signaling URL mismatch and a missing
   PHP extension. This supports targeted connection/error diagnostics; it does
   not prove the same implementation bug exists in Cassini.
3. **A reachable deploy daemon is not a successful ExApp deployment.** In
   [Podman: HaRP obscurity](https://help.nextcloud.com/t/podman-harp-obsucrity/247908),
   9 August 2026, the UI connection test succeeds but test deployment fails.
   Replies discuss image availability and the meaning of local/remote sockets
   and ports. Treat the cause as unresolved in this evidence; do not label every
   such failure a socket-permission error. Guide users through an actual test
   deployment, not just a green connection indicator.
4. **AIO can still have a remote execution environment.** A [23 June 2026
   thread](https://help.nextcloud.com/t/246172) describes AIO on Ubuntu and an
   external Docker engine reached through HaRP/FRP. The test container starts
   remotely but liveness fails. “Uses AIO” does not establish where the recorder
   runs or which host a command should target.
5. **Managed users are present.** An [8 August 2026 Hetzner
   report](https://help.nextcloud.com/t/247893) explicitly describes managed
   Nextcloud; another participant reports the same hosted situation. This is
   evidence that a provider path belongs in the UX, not a measurement of that
   audience or proof of every provider's ExApp policy.
6. **ARM and appliance variants are real.** A [12 April 2026 NextcloudPi
   report](https://help.nextcloud.com/t/243248) identifies a Raspberry Pi image
   and `aarch64`, and explicitly says the author has SSH access. An [August
   TrueNAS report](https://help.nextcloud.com/t/248743) identifies a recent
   TrueNAS/Nextcloud deployment. Do not equate appliance users with having no
   shell. Check the proposed recorder host's architecture: an ARM Nextcloud
   server could use a separate compatible recorder host.
7. **AIO restart handling is a justified check.** A [13 February 2026 AIO
   report](https://help.nextcloud.com/t/240295) contains startup logs deleting
   `spreed recording_servers`. This corroborates the startup behavior documented
   in our proposal. The post is about Collabora, so it is not evidence that the
   author had installed a custom recorder or that our workaround was tested.

## Comparison with the implemented proposal

| Proposal element | Assessment | Adjustment |
| --- | --- | --- |
| Actionable checks and forms beneath each check | Good fit for diagnosable failures | Keep; provide evidence and a specific next step |
| AIO/custom checkbox | Too coarse, and AIO is preselected without evidence | Separate installation method, execution host and administrator access |
| Custom `sudo -u www-data php occ` | Fits only a subset of host installations | Platform-aware instructions with explicit target host, container/service and user |
| Bash/curl/jq handoff as the primary route | Skills and access are assumed | Prefer a verified in-app operation where supported; otherwise exact guided steps or a provider request |
| In-app checks only | Cannot help people whose ExApp never starts | Add a pre-install eligibility guide outside Cassini |
| Public identity vs configured connection backend | Matches multi-service and proxy examples | Keep, and identify which connection failed |
| HPB/internal-secret prerequisites | Must be explained before asking for a value | Check whether HPB exists, then explain where to obtain its internal secret |
| CPU support | Reduces a prerequisite | Keep; distinguish CPU support from processor-architecture support |
| amd64 published images | Excludes execution on an ARM-only recorder host | State this before installation; evaluate arm64 support separately |
| Persistent config plus real Talk test | Matches restart and integration risks | Keep historical playback distinct from live connectivity |

## Recommended next iteration

Prioritize AIO, other Docker/Compose installations, and host/manual installations
for tested repair paths. Include managed/no-host-access and unknown choices from
the start. Treat NAS as another dimension of those installations, with named
platform guidance where verified. Offer honest next steps for Snap, Podman and
Kubernetes rather than implying that adding an occ command guarantees support.

Use four independent pieces of information:

1. **Capabilities:** Nextcloud/Talk availability, deploy-test outcome, configured
   HPB, accepted credentials, recording reachability and storage/processing.
2. **Installation method:** explicit authoritative metadata if available;
   otherwise a short confirmation. Cassini's own Docker environment is not proof
   of Nextcloud's installation method.
3. **Execution target:** where the ExApp/recorder and HPB actually run, including
   remote deployment engines. Architecture checks belong to this target.
4. **Authority and access:** can this person change Nextcloud settings, access a
   host/container terminal, or only contact the provider? Ask rather than infer.

For each missing prerequisite show: what is needed; what evidence is available;
where to obtain it for the confirmed environment; who can supply it; and a
recheck action. A provider request should describe the required capability and
safe diagnostic evidence, not request transmission of secrets through a ticket.
Do not promise an in-app configuration API until its availability and permission
boundary have been verified.

## Conclusions from the agent report that were not accepted

- Exact deployment market shares, historical adoption trends and universal
  administrator-skill labels derived from support-topic classification.
- “More than 65% of Talk users lack HPB,” “more than 40% use external proxies,”
  and an estimated 5–15% ARM exclusion: no defensible reproduced denominator.
- Categorical impossibility claims about Snap, bare-metal or managed Nextcloud:
  the actual available deployment target, permissions and provider policy matter.
- AIO detection from the ExApp's environment alone, or a claim that a native
  configuration push is already available merely because an admin is signed in.

The evidence supports prioritizing and testing distinct paths. It does not yet
support a credible percentage of Nextcloud users who can install Cassini or run
the current handoff script successfully.
