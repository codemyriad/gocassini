import { describe, expect, it } from "vitest";
import { get } from "svelte/store";
import { LL, operatorPlace } from "cassini-viewer/i18n";

import noticeSource from "./SetupNotice.svelte?raw";

// Source-level assertions, the convention this repo follows for .svelte files:
// the suite runs in node with no DOM harness. What the notice SAYS is decided in
// operator/setupHealth.ts and tested there; what is asserted here is its shape:
// one row, the tone, and one way on for each audience (#500).

describe("the setup notice", () => {
  it("takes its tone rather than assuming the loud one", () => {
    expect(noticeSource).toContain('export let tone: SetupNoticeTone = "warning";');
    // A notice about a check that has not run yet must not look like a fault.
    expect(noticeSource).toContain("{tone === 'warning' ? 'alert-warning' : ''}");
    expect(noticeSource.match(/<TriangleAlert/g) ?? []).toHaveLength(1);
    expect(noticeSource.match(/<Info/g) ?? []).toHaveLength(1);
  });

  it("is a strip with no recipe in it: the steps live in Doctor", () => {
    expect(noticeSource).not.toContain('class="card');
    expect(noticeSource).toContain('class="alert items-start gap-3 py-2');
    expect(noticeSource).not.toMatch(/<details[\s>]/);
    expect(noticeSource).not.toContain("notice.steps");
    expect(noticeSource).not.toContain("<pre");
  });

  // One button: Doctor re-runs the same check Try again used to, and what is
  // fixed there re-reads this notice (setupSignal.ts).
  it("offers an administrator one way on: Doctor", () => {
    expect(noticeSource).toContain("$: isAdmin = notice.admin;");
    expect(noticeSource).not.toContain("try_again");
    expect(noticeSource).not.toContain('dispatch("retry")');
    expect(noticeSource).toContain('dispatch("navigate", "doctor")');
    expect(get(LL).app.shell.setup_notice.open_doctor({ place: operatorPlace("doctor") })).toBe("Open Operator › Doctor");
  });

  // Everyone else: what still works, and the page's address to pass on, copied
  // rather than printed. No Try again: it would re-run a check whose answer
  // they are not shown.
  it("gives everyone else a copy button, and the address only if copying fails", () => {
    expect(noticeSource).toContain("navigator.clipboard.writeText(notice.shareUrl)");
    expect(noticeSource).toContain("{:else if notice.shareUrl}");
    expect(noticeSource).toContain('{#if copyState === "failed"}');
    expect(get(LL).app.shell.setup_notice.copy_link()).toBe("Copy link for an administrator");
  });
});
