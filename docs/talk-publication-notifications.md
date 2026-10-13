# Talk publication notifications

After the first successful publication of a Talk recording, Cassini posts
a named viewer link in its originating room and in **Shared items → Other**.
For example: `Weekly planning · 13 Oct · 32 min`.
The author is the moderator who started recording (`start.owner`), with that person's name and
avatar. Posting uses the existing AppAPI credentials; no bot, account, app
password, extra membership, or new manifest route is required.

The message opens Cassini's embedded meeting viewer. Its public Nextcloud base
comes from the original recording request, not an internal callback/OCS host.
Nextcloud Files continues to decide who can open the recording. Posting the
message creates no Files shares and grants no new access. Talk indexes the same
rich-object message in its sidebar; it is not a native recording attachment.
The label is visible to room members, including later joiners, but the viewer
still checks their Files access. The wording makes no claim
that transcription succeeded: recordings without transcripts also get a link.

Delivery is best effort. After the first successful publication, the publish
worker makes one Talk POST with a 15-second timeout. A refusal, timeout, malformed
response, or shutdown can leave the recording without a notification. Cassini
logs the failure and continues publication cleanup; it never retries the message
or recovers notification work after a restart.

```text
Upload recording + establish Files permissions
                    |
        Record publication success
                    |
       One bounded rich-object POST as moderator
               /           \
          accepted         failed/unconfirmed
               |                 |
               |             log failure
               +--------+--------+
                        |
              Continue publish cleanup
```

Only Talk-bound recordings delivered through the Nextcloud Files sink are
announced. Existing successful publication attempts suppress announcements on
republish, including recordings published before this feature was installed.
Duplicate deliveries of the publish job are already rejected by the publication
claim. There is no additional SQLite migration, notification table, queue,
delivery-status UI, or chat-history reconciliation. There is no exactly-once
delivery guarantee.

If a notification is missing, a participant can copy the meeting URL from
Cassini and post it manually. A notification failure does not change publication
success or trigger a fallback to another sender.

The `highlight` rich object is sent through `/chat/{token}/share` with a
meeting ID, a display name and the existing viewer link. Talk renders a bold
link in chat and a named link under Shared items → Other. It does not provide
a custom metadata card.

## Validation

Run backend tests from `cassini-operator/`:

```sh
go test -race ./...
```

On macOS, if the existing doctor test reports a `/var` versus `/private/var`
path mismatch, use `TMPDIR=/private/tmp go test -race ./...`.

An opt-in API test creates disposable private, public and one-to-one rooms on
an **already installed local ExApp fixture**, sends as its administrator, checks
the author via chat history and the same message ID/link in Shared items,
and cleans up the rooms and temporary peer account. It refuses non-loopback URLs. Supply these environment variables
from the local fixture without writing credentials into source control:

- `CASSINI_TEST_TALK_URL`: for example `http://127.0.0.1:28080`.
- `CASSINI_TEST_APP_ID`, `CASSINI_TEST_APP_SECRET`, `CASSINI_TEST_APP_VERSION`:
  the installed ExApp's values.
- `CASSINI_TEST_ADMIN_PASSWORD`: the local fixture's `admin` password, used only
  to provision/remove the disposable test peer. Talk requests use AppAPI act-as.

Then, from `cassini-operator/`:

```sh
go test ./internal/operator -run '^TestTalkNotificationInstalledExApp$' -count=1 -v
```

To validate the complete product flow after deployment, record a short call,
stop it, wait for publication, and check that the room receives one link authored
by the recording moderator. Open the link as a permitted reader, and verify an
unshared account cannot read the meeting. Republish the job and confirm there is
no second message. Verify the same link appears under Shared items → Other. Native file
attachments and transcript-range links remain separate work.

## Label metadata

The title, date and duration come from the matching published catalog entry.
Dates use a compact day/month label (for example, “13 Oct”) without converting
the recorded date to server time. The same compact label appears in chat and
Shared items. The Cassini prefix and speaker list are omitted to keep it short.
Missing or unreadable metadata is omitted; the fallback is “Meeting available”.
Text is whitespace-normalized and bounded. This works for both Opus and JSON
publications and does not claim a transcript or summary exists. Talk supplies
the external-link arrow; it is not duplicated in the label.

The change applies to future publication messages. Existing chat messages are
not edited or backfilled. Delivery remains one best-effort request with no retry.
