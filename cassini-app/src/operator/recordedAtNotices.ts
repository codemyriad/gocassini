import { get } from "svelte/store";
import { LL } from "cassini-viewer/i18n";
import type { RecordedAtNotifications } from "./recordedAtMigration";

// The sentences under "What this does in Nextcloud" in the recordedAtUtc
// migration panel, in the reader's language. They mirror recordedAtNotices in
// cassini-operator recorded_at_migration.go branch for branch: the operator
// reads Nextcloud's capabilities and sends the facts (known, versioning,
// activity_app, notifications_app, web_push, browser_push, account), and
// this words them. The operator's English `details` are no longer shown.
export function recordedAtNoticeLines(n: RecordedAtNotifications): string[] {
  const t = get(LL).app.operator.recorded_at_migration.notices;
  const lines: string[] = [t.saved_again({ account: n.account })];
  if (!n.known) {
    return [...lines, t.unreadable(), t.unreadable_defaults(), t.email_defaults()];
  }
  lines.push(n.versioning ? t.versioning_on({ account: n.account }) : t.versioning_off());
  if (n.activity_app) lines.push(t.activity_on(), t.email_defaults());
  else lines.push(t.activity_off());
  if (!n.notifications_app) {
    lines.push(t.notifications_off());
  } else if (n.activity_app) {
    const push = n.browser_push ? t.web_push_browsers() : n.web_push ? t.web_push_apps() : "";
    lines.push(push ? `${t.notifications_on()} ${push}` : t.notifications_on());
  }
  // Notifications without Activity: nothing creates a notification, and
  // the Activity line has already said the change is in nobody's stream.
  return lines;
}
