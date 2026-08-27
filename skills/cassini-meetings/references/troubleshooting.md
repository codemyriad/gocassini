# Cassini meetings troubleshooting

Read this reference after a command fails, returns an empty or incomplete
catalog, or reports access provenance other than `nextcloud-files`. Interpret
the evidence before retrying.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success, including `list` with no readable meetings |
| `1` | Runtime failure such as rejected credentials, unavailable Nextcloud Files, an unreadable meeting, or no recording readable at an id |
| `2` | Usage or configuration error such as a missing argument, invalid date, or backwards range |

## Results and failures

| Evidence | Meaning and response |
|---|---|
| `meetings=0` with a mis-provisioned note | The account may have no readable recordings, or the recordings folder may be unavailable or mis-provisioned. The server cannot distinguish them. Report both possibilities and suggest checking the same account in the Cassini viewer. Do not retry blindly. |
| `your filter excluded all N` | The account can read `N` meetings, but the filter matched none. Widen or remove the filter, or get an exact selector from `meetings rooms`. Do not call this a permission or provisioning failure. |
| `note=N meeting(s) have a date this build cannot read` | Dated filters omitted entries with unparseable `dateLabel` values. List without date filters if one could answer the request. |
| Nonzero `.skipped` | Malformed catalog entries were discarded. State that the listing is incomplete rather than treating it as exhaustive. |
| A room selector returns nothing | `--room` is exact. Copy `.rooms[].room`; names and substrings are not selectors. |
| Duplicate room names | Old and new recordings may have split one apparent conversation between two derived ids, but the data cannot prove it. Query both and disclose the ambiguity. Merging is an administrator action. |
| A visible meeting has no selectable room | The recording has no room metadata. List without `--room`; do not say it is missing. Restoring attribution is an administrator action. |
| `list configuration error: --from …` | The date format is invalid or the range is backwards. Use `YYYY-MM-DD`, optionally followed by ` HH:MM` or ` HH:MM:SS`, without a timezone. |
| `fetchable=no` | The meeting predates the portable single-file format. `context` and `fetch` cannot retrieve it. Do not retry. |
| `no recording you can read at that id` | The recording is absent or unreadable by this account; these cases are intentionally indistinguishable. Run `meetings list`. Never claim it does not exist or identify a permission denial. |
| `Nextcloud rejected the credentials` | The app password is wrong, revoked, or belongs to another account. Ask the user to create a new app password; never print its value. |
| `Nextcloud Files is unavailable` | This is an upstream outage, not evidence of bad credentials or permissions. Retrying later is reasonable. |
| `source=unknown`, `source=unrecognised`, or another unexpected source | Per-caller Nextcloud Files access control is not established. Warn before using the results as permission-filtered. |
| `refusing to follow a redirect` | The CLI protected the credential from being sent to a redirected destination. Do not bypass it; correct or report the configured Nextcloud URL. |
| `points outside the Nextcloud you configured` | A catalog entry named another host. Do not work around the refusal; report a possible export misconfiguration or credential-harvesting attempt. |
| `is empty (0 bytes)` | The published recording is broken. Report it; retrying will not repair it. |
| `ffprobe … not found` | Install ffmpeg to use `meetings context`. `rooms`, `list`, and `fetch` remain available. |

## Stopping rules

- Do not retry credential failures, unselectable legacy recordings, empty
  published files, or access-protection refusals without a relevant external
  change.
- Retry an upstream availability failure later, not in a tight loop.
- Do not use broader credentials, alternate hosts, redirects, or administrator
  operations to get around the account's visible catalog.
- The meeting surface is read-only. Starting, stopping, deleting, re-running,
  merging, or reattributing recordings requires separate administrator action
  and authorization.
