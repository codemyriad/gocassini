import { defineConfig } from "@playwright/test";

// Browser tests for source capture. Chromium only, and deliberately so: the
// chain needs getUserMedia with a fake device, WebRTC encoded transforms and
// OPFS sync access handles, and Chromium is the one engine where all three are
// drivable headlessly today. Firefox has no MediaStreamTrackProcessor and
// WebKit is not scriptable for fake capture the same way, so a cross-browser
// matrix here would be three-quarters skips pretending to be coverage.
export default defineConfig({
  testDir: "./e2e",
  // These drive a real call: a few seconds of audio each, plus service-worker
  // registration.
  timeout: 60_000,
  expect: { timeout: 15_000 },
  // One at a time. The tests share an OPFS-backed origin per worker and the
  // fake audio device is a process-wide Chromium flag; parallelism buys
  // seconds and costs determinism.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: process.env.CI ? "list" : "line",
  use: {
    headless: true,
    launchOptions: {
      args: [
        // A synthetic microphone, so the test needs no fixture media and every
        // run captures the same thing.
        "--use-fake-device-for-media-stream",
        "--use-fake-ui-for-media-stream",
        // Never let a test play real audio on a developer's machine.
        "--mute-audio",
        "--autoplay-policy=no-user-gesture-required",
      ],
    },
  },
});
