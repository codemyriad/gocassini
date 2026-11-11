import { get } from "svelte/store";
import { describe, expect, it } from "vitest";
import { LL } from "cassini-viewer/i18n";
import { recordedAtNoticeLines } from "./recordedAtNotices";
import type { RecordedAtNotifications } from "./recordedAtMigration";

// Each branch of recordedAtNotices in cassini-operator recorded_at_migration.go.
const t = () => get(LL).app.operator.recorded_at_migration.notices;
const known = (over: Partial<RecordedAtNotifications>): RecordedAtNotifications => ({
  known: true, account: "cassini", activity_app: true, notifications_app: true, web_push: false, browser_push: false, versioning: true, ...over,
});

describe("recordedAtNoticeLines", () => {
  it("says what Nextcloud does by default when the capabilities could not be read", () => {
    expect(recordedAtNoticeLines({ known: false, account: "cassini", error: "anything" })).toEqual([
      t().saved_again({ account: "cassini" }), t().unreadable(), t().unreadable_defaults(), t().email_defaults(),
    ]);
  });

  it("names the account that saves the file", () => {
    const [first, second] = recordedAtNoticeLines(known({ account: "recorder" }));
    expect(first).toBe("Each recording is saved again by Cassini's recorder account, over the same file: its shares, links and file id stay as they are, and the audio is unchanged.");
    expect(second).toBe(t().versioning_on({ account: "recorder" }));
  });

  it("with everything enabled: versioning, the activity entry, the defaults, then notifications", () => {
    expect(recordedAtNoticeLines(known({}))).toEqual([
      t().saved_again({ account: "cassini" }), t().versioning_on({ account: "cassini" }), t().activity_on(), t().email_defaults(), t().notifications_on(),
    ]);
  });

  it("says when versioning and Activity are off", () => {
    expect(recordedAtNoticeLines(known({ versioning: false, activity_app: false, notifications_app: false }))).toEqual([
      t().saved_again({ account: "cassini" }), t().versioning_off(), t().activity_off(), t().notifications_off(),
    ]);
  });

  it("says nothing about notifications when the app is on but Activity is off", () => {
    expect(recordedAtNoticeLines(known({ activity_app: false }))).toEqual([
      t().saved_again({ account: "cassini" }), t().versioning_on({ account: "cassini" }), t().activity_off(),
    ]);
  });

  it("says notifications are off before reading push", () => {
    const lines = recordedAtNoticeLines(known({ notifications_app: false, web_push: true, browser_push: true }));
    expect(lines.at(-1)).toBe(t().notifications_off());
  });

  it("adds web push to the notifications sentence, for browsers or not", () => {
    expect(recordedAtNoticeLines(known({ web_push: true, browser_push: true })).at(-1)).toBe(`${t().notifications_on()} ${t().web_push_browsers()}`);
    expect(recordedAtNoticeLines(known({ web_push: true, browser_push: false })).at(-1)).toBe(`${t().notifications_on()} ${t().web_push_apps()}`);
    expect(recordedAtNoticeLines(known({ web_push: false, browser_push: true })).at(-1)).toBe(`${t().notifications_on()} ${t().web_push_browsers()}`);
  });
});
