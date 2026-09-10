# Disposable Chrome network lab

Run from the checkout:

```bash
node harness/bin/network-lab.mjs
# Or start at the demo / a particular Talk room:
node harness/bin/network-lab.mjs --url https://demo.nextcloud.codemyriad.io/call/pzm5eh5g
```

Requires the installed Google Chrome and this checkout's npm dependencies
(`npm ci`). The launcher opens a dedicated Chrome profile with a test tab and a
**Network lab** control tab. Use your real microphone and ordinary Chrome
permission prompts. Sign in to Nextcloud in the test tab; the profile shares no
cookies with your regular browser. Join with the same Nextcloud account to test independent browser sessions, or
different accounts to test different participants. The same-account case requires
the session-aware recorder and capture companion from this branch.

The controls apply immediately to the test tabs. They include normal networking,
poor/very poor connections, WebRTC packet loss, slow recording uploads, and
disconnection. Custom controls accept latency in milliseconds, bandwidth in
**kbit/s**, and WebRTC loss in percent. Zero bandwidth means unlimited.
**Restore normal** removes the simulation without reloading the call. The control
tab is excluded, so it can restore a test tab that is offline. Keep the control
tab for controls and use the other tab for the call.

Suggested experiments:

1. **Lost live speech:** start a recorded call while networking is normal, choose
   **Media packet loss**, speak, then restore normal before stopping recording.
   Watch Cassini's Participant audio section from your regular browser to compare
   upload arrival with actual use in the meeting audio and transcript.
2. **Slow post-call delivery:** choose **Slow recording upload** before stopping
   recording. Only the `/operator/capture/upload` requests are throttled; Talk
   signaling, capture registration and live media keep their normal connection.
   At 32 kbit/s, a 1 MB upload takes about four minutes. Watch the two-minute wait
   and late-upload rebuild; restore normal to release the upload sooner.
3. **Disconnection:** click **Disconnect**, then restore normal while keeping the
   tab open. HTTP requests fail and WebRTC packets are dropped. This is Chrome's
   network emulation, not a disabled operating-system interface; established
   WebSocket behavior is not a reliable simulation of a physical link failure.
   A failed upload can remain buffered until a Talk-page reload retries it.

Close the dedicated Chrome window or interrupt the launcher to stop. No host
traffic-control, firewall, proxy or system network configuration is changed.

Each launch creates a fresh profile under `harness/runtime/network-lab/chrome-*`.
Profiles are retained on exit because they can contain OPFS audio awaiting upload.
To recover one, close its running Chrome first, then launch:

```bash
node harness/bin/network-lab.mjs --profile /absolute/path/to/chrome-profile
```

Once its audio is delivered, that profile directory can be deleted. The directory
contains `network-events.jsonl`: condition changes and capture request/response
metadata, without audio bodies. The most recent launch details are in
`harness/runtime/network-lab/latest.json`. The controller binds to loopback port
28181; use `--port 0` for a free port. Its printed URL includes a random control
token. Closing the window stops the controller as well.

The implementation uses Chrome's
[`Network.emulateNetworkConditionsByRule`](https://chromedevtools.github.io/devtools-protocol/tot/Network/#method-emulateNetworkConditionsByRule).
An empty global pattern applies to WebRTC too; URL-specific rules restrict HTTP
upload throttling. The launcher establishes a normal throttling profile **before
navigation and socket creation**, which is needed to change an established
WebRTC connection later. Unsupported Chrome versions fail at startup instead of
silently claiming to simulate media loss. New tabs are attached as they open;
for deterministic results, start the call in the initial test tab.

Run the real-Chrome verification headlessly:

```bash
node --test harness/network-lab/network.test.mjs
```

It measures HTTP latency and upload throughput, verifies that established WebRTC
data-channel traffic stops at 100% packet loss and recovers, checks upload-only
scope, and restores an offline test tab through the unaffected control tab.
