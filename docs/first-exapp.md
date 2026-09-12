# Is Cassini your first external app?

Cassini runs as an external app (ExApp). AppAPI connects Nextcloud to a
deployment service, called a deploy daemon, which runs the app. Installing
AppAPI alone does not prepare that service.

This page helps you check that foundation and return to installing Cassini.
Use the upstream instructions for your Nextcloud version when configuring the
deployment service; select the version in the Nextcloud documentation sidebar.

## Check the deployment service

As a Nextcloud administrator, open **Administration settings → AppAPI**.

- **A daemon is already registered:** use the one intended to run Cassini.
  If it has a recent successful Test deploy and its configuration has not
  changed, continue with [Cassini installation, Step 2](exapp-install.md#step-2--pick-an-image-tag).
  Otherwise run the test below. You do not need a separate daemon for Cassini.
- **No daemon is registered and you use AIO:** open AIO management and check
  whether its integrated HaRP component is available and enabled. Follow
  [AIO/AppAPI setup](https://docs.nextcloud.com/server/latest/admin_manual/exapps_management/AppAPIAndExternalApps.html#setup-deploy-daemon),
  then return to the AppAPI page. Older or custom AIO versions may differ.
- **No daemon is registered on another installation:** the server administrator
  needs to configure a deployment service using
  [Nextcloud's AppAPI setup guide](https://docs.nextcloud.com/server/latest/admin_manual/exapps_management/AppAPIAndExternalApps.html#harp).
  For HaRP, choose its documented local or remote deployment example to match
  where the ExApps will run. Return here after registration.
- **You cannot access these settings or someone else manages the services:**
  send the request below to that administrator or provider.

Another working ExApp is useful evidence, but it may use a different daemon or
execution host. Cassini's published image currently needs a Linux amd64
execution host; CPU transcription works without a GPU. Check the host selected
for Cassini, which may differ from the Nextcloud host.

## Run Test deploy

In the intended daemon's three-dot menu, select **Test deploy** and start the
test. This installs Nextcloud's small test app on that daemon. Wait for all
stages through **Enabled** to succeed. A successful **Check connection** alone
is not this test.

If it succeeds, continue with [Cassini installation, Step 2](exapp-install.md#step-2--pick-an-image-tag).
This verifies the ExApp foundation; Cassini Setup will separately verify Talk,
storage and a real recording. Keep using the tested daemon when registering
Cassini.

## If the test fails

Note the first failed stage and use the test's **Download logs** action if
available. The [upstream Test Deploy guide](https://docs.nextcloud.com/server/latest/admin_manual/exapps_management/TestDeploy.html)
explains the stages and log locations. These are starting points for diagnosis,
not automatic conclusions about the cause:

| First failed stage | Next useful check |
| --- | --- |
| Register | Check AppAPI's error and the daemon registration in Nextcloud. |
| Image Pull | Check the image error on the selected execution host: architecture, registry access and deployment-service errors can all matter. |
| Container Started | Inspect the test container's startup log on that host. |
| Heartbeat | Check the route from Nextcloud to the test app, as well as the app's startup log. |
| Init | Check the test app's callback to Nextcloud, including the configured URL and certificate trust. |
| Enabled | Inspect Nextcloud and test-app logs for the enable callback and registration errors. |

For HaRP routing failures, use the
[routing guidance](recording-readiness.md#harp-routing-and-missing-navigation).
Identify which host makes the failing request: `localhost` inside a container
is that container. A URL working in your browser does not prove the test app
can reach it. Preserve subdirectories and use the certificate error to repair
trust rather than disabling TLS verification.

After a repair, repeat Test deploy on the same daemon. If it succeeds but
Cassini still fails to install or open, inspect **Cassini's** deployment and
app logs with the [installation guide](exapp-install.md); record that the
generic test passed. That distinction helps locate the remaining failure.

## Ask for help with the failed step

Copy this request and fill in the brackets. Review any attached logs for
credentials before sharing them; no secrets are needed in the request.

> I am preparing to install Cassini as a Nextcloud external app.
> AppAPI's Test deploy on the intended Cassini daemon is [not available / failing
> at stage / successful]. Nextcloud version: [version]. AppAPI version: [version].
> Installation method, if known: [method]. The ExApp execution host is
> [same as Nextcloud / separate / unknown], with architecture [value / unknown].
> Please help complete Test deploy through Enabled on a Linux amd64 execution
> host. Cassini can transcribe on CPU; a GPU is optional.
> I can provide the failure time and a reviewed log excerpt separately.
> Once the test passes, I will resume the Cassini installation and recording checks.

If the provider also needs to establish Talk recording prerequisites, use the
[Cassini eligibility request](before-installing.md#request-for-the-administrator-or-provider).
