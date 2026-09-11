# Before installing Cassini

You should not have to install Cassini successfully to discover its prerequisites.
This guide is for the person evaluating an installation and the administrator or
provider who can configure its services. These are things to verify, not automatic
detection results.

## Identify who can make the changes

Nextcloud administrator access does not necessarily include a host terminal or
permission to create services. If someone else manages the server, send them the
request below. If you have only a NAS/container console, establish which host
actually runs Nextcloud before using host commands.

AIO, Compose and NAS are overlapping descriptions: an AIO deployment can use
Compose on a NAS. Identify the Nextcloud installation method, the ExApp execution
host and the Talk signaling server separately; they need not be the same machine.

## Check eligibility before attempting deployment

| Needed | Where to check or obtain it | Who can resolve it |
| --- | --- | --- |
| A Nextcloud version supported by the release | Compare Administration → Overview with the release manifest; the current branch requires Nextcloud 32–35 | Nextcloud administrator/provider |
| AppAPI enabled and a working deployment engine | Administration → AppAPI; verify an actual Test deploy completes, not only the connection check | Administrator of the deployment engine |
| A compatible execution host | Published Cassini images currently target Linux amd64. Check the host where the ExApp will execute, which can differ from Nextcloud's host. CPU transcription is supported; a GPU is optional | Execution-host administrator |
| Talk HPB with media support and internal-client authentication | Talk administration and the signaling service's configuration. A working ordinary Talk call is insufficient evidence | Talk/HPB administrator/provider |
| A route from Talk to the recording backend, and Cassini to Nextcloud/HPB | Confirm the intended public and internal addresses, TLS and proxy routing | Network/service administrator |

For AIO, its management interface provides the relevant components; enable and
verify what is needed. For other deployments, arrange a supported ExApp execution
engine and HPB rather than assuming the Nextcloud installation already includes
them. For Snap, Podman, Kubernetes or a vendor NAS package, check that deployment's
supported integration path. A different occ wrapper alone does not establish
ExApp compatibility.

If an ARM machine runs Nextcloud, that does not itself rule out a separate amd64
execution host. Running the published image directly on an ARM-only execution host
is not supported by the current release. Do not assume emulation is a validated
installation path.

Once eligible, follow [the installation guide](exapp-install.md), then use Cassini
Setup to check credentials, storage, processing, and a real Talk recording. The
[recording setup guide](recording-readiness.md) explains the checks and restart
behavior. Storage onboarding must follow D-708's automatic simple-mode direction
when that work is integrated; access disclosure is not another recording prerequisite.

## Request for the administrator or provider

Copy this text and add the public Nextcloud address separately:

> I would like to use Cassini to record and transcribe Nextcloud Talk meetings.
> Can you confirm whether this instance has a supported Nextcloud version,
> AppAPI with a working ExApp deployment engine, a Linux amd64 execution host,
> and Talk HPB with media support and internal-client authentication?
>
> Please identify who manages Nextcloud, the deployment engine and the Talk
> signaling server, and whether you can configure Talk to call Cassini. Please
> preserve any current recording-backend settings and make changes persistent
> across restarts. If these capabilities are unavailable, please tell me which
> can be provided and what installation path you support.
>
> Please enter required credentials directly through the agreed secure
> configuration process; do not send secrets in this ticket. After installation,
> I would like help completing Cassini's connection checks and a short Talk test
> recording with playback confirmation.
