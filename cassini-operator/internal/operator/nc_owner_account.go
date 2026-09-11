package operator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// Creating the `cassini` service account on the enabled edge (D-754).
//
// Every recording is written and read as that one account, in BOTH storage
// modes: the Team folder's owner and the private home tree are the same
// identity. So an install without it records nothing at all, whichever mode it
// resolves to, and an instance with neither native app still needs it — which
// is why this attempt runs before the mode is resolved rather than inside
// either mode's branch.
//
//	probe says the account is missing
//	  ├── POST /cloud/groups  cassini          the narrow owner group
//	  ├── POST /cloud/users   cassini          random password, then dropped
//	  ├── POST /cloud/users/cassini/groups     membership, when the create
//	  │                                        could not carry it
//	  └── read both back, and report what is still missing
//
// One attempt per preflight run, never a loop. On Nextcloud 34.0.2 and later
// each of those writes is `#[PasswordConfirmationRequired]` and an ExApp can
// never satisfy it (D-638, D-659): the middleware reads `last-password-confirm`
// out of a PHP session an act-as-user request does not have. Older releases,
// and instances whose administrator has widened
// `allowed_no_password_confirmation_ranges` to cover this app, answer 200 and
// the account appears. Attempting it costs a handful of requests on a fresh
// install and nothing at all afterwards.
//
// A refusal is an OUTCOME, not a failure: the administrator's own browser does
// the same two writes from a real session, where Nextcloud's password
// confirmation dialog works as designed, and the app has that path. So a
// refused attempt reports `owner_account` as the one remaining prerequisite and
// stops.
//
// The judgement is on the OCS envelope, never the HTTP status. A refused
// provisioning write can arrive as `HTTP 200` with the failure only in
// `ocs.meta` (see ocsRefusal), and reading that as a success is how an account
// that was never created gets reported as made — followed by every act-as
// call 401ing for the life of the install.
//
// The password is minted here with crypto/rand, spent on the one request that
// requires it, and dropped. Nothing logs it, nothing stores it and no response
// carries it: the operator authenticates as the account through AppAPI act-as,
// which presents no password, and an administrator who ever needs one resets it
// (`occ user:resetpassword cassini`).

// ncRecordingsOwnerDisplayName is what the account is called in Nextcloud's own
// user list, so an administrator reading it knows what made it.
const ncRecordingsOwnerDisplayName = "Cassini recordings"

// What one attempt ended in. Machine-readable in the same spirit as
// appInstallOutcome: each reason leads somewhere different.
const (
	// ownerAccountPresent means the probe already found the account, so nothing
	// was written.
	ownerAccountPresent = "present"
	// ownerAccountCreated means the account and its owner group are in place.
	ownerAccountCreated = "created"
	// ownerAccountNeedsPassword means Nextcloud demanded password confirmation.
	// The expected answer on 34.0.2+, and the reason the browser path exists.
	ownerAccountNeedsPassword = "password_confirmation_required"
	// ownerAccountGroupIncomplete means the account exists and its owner group
	// or its membership does not. Named apart from the rest because the two
	// halves come apart in practice and the remedies differ: the default model
	// works without the group, the access-controlled one mounts onto it.
	ownerAccountGroupIncomplete = "group_incomplete"
	// ownerAccountFailed is anything else: unreachable, a 500, or a write that
	// answered like a success and left nothing behind.
	ownerAccountFailed = "failed"
)

// ownerAccountOutcome is one attempt's result. Detail is the sentence an
// administrator reads, and is empty when there was nothing to do.
type ownerAccountOutcome struct {
	Reason string
	Detail string
}

// ensureServiceAccountOnEnable creates the service account and its owner group
// when the probe reports the account missing, and updates the probe with what
// is true afterwards.
//
// The probe is amended rather than re-run: the facts it carries are what every
// later gate reads (sanity, the setup plan, /storage's account row), so an
// account this call just created has to be in them or the run would report a
// prerequisite it had satisfied a moment earlier.
//
// One part of it IS re-run: the two recordings roots, which answer as the
// service account and were therefore skipped entirely by a probe taken while
// there was none. They are what the mode is resolved from, so leaving them empty
// would trade a created account for an unresolvable install.
//
// It never returns an error. A refused create is a reported state, not a
// failure of the preflight, and the caller continues to resolving the mode:
// an install whose account is missing still has a mode, and that mode is what
// the first-run dialog is about to act on.
func (c ExAppConfig) ensureServiceAccountOnEnable(ctx context.Context, client *http.Client, probe *ncStorageProbe, logger *log.Logger) ownerAccountOutcome {
	if probe.ServiceAccount {
		return ownerAccountOutcome{Reason: ownerAccountPresent}
	}
	logger.Printf("nc storage: the %q service account is missing; attempting to create it and the %q group as %q",
		ncRecordingsOwner, ncRecordingsOwnerGroup, probe.AdminUser)

	var refusal string
	// The group first: the account create carries `groups[]`, so a group that
	// exists by then saves the separate membership write.
	if !probe.OwnerGroup {
		created, r := c.createOCSGroup(ctx, client, ncRecordingsOwnerGroup)
		switch {
		case r != "":
			refusal = r
		case created:
			logger.Printf("nc storage: created the %q group", ncRecordingsOwnerGroup)
		}
	}
	created, r := c.createRecordingsOwner(ctx, client)
	switch {
	case r != "":
		// The account's refusal is the one worth reporting: the group is only
		// ever refused by the same middleware, and the account is the
		// prerequisite both modes rest on.
		refusal = r
	case created:
		logger.Printf("nc storage: created the %q service account", ncRecordingsOwner)
	}

	// Read both back rather than believing either write. A refusal can wear an
	// HTTP 200, and a refusal says nothing about whether an administrator had
	// already created the account by hand — which is the documented recovery,
	// and the state a re-enable on a prepared instance is in.
	if exists, err := c.userExists(ctx, client, ncRecordingsOwner); err != nil {
		logger.Printf("nc storage: re-check service account %q: %v", ncRecordingsOwner, err)
	} else {
		probe.ServiceAccount = exists
	}
	if exists, err := c.groupExists(ctx, client, ncRecordingsOwnerGroup); err != nil {
		logger.Printf("nc storage: re-check owner group %q: %v", ncRecordingsOwnerGroup, err)
	} else {
		probe.OwnerGroup = exists
	}

	if probe.ServiceAccount {
		// The account exists NOW and did not when this function was entered.
		// Recorded, because it is the one fact that proves an install is fresh
		// no matter what Nextcloud is showing: both models write and read every
		// recording as this account, so one that did not exist a minute ago
		// owns nothing anywhere (storageModeFromProbe).
		probe.ServiceAccountCreated = true
		// And the probe carries no archive facts at all: both roots are read as
		// this account, and the probe skips them when there is none. Without
		// this the mode cannot be resolved on the very edge that made the
		// install able to record (ArchivesComparable() stays false,
		// storageModeFromProbe answers storage_mode_unresolved), and a fresh
		// install publishes nothing until somebody enables the app a second
		// time.
		c.probeArchives(ctx, client, probe, logger)
	}

	var membershipErr error
	if probe.ServiceAccount && probe.OwnerGroup {
		// An account that already existed, or one whose group was created after
		// it, is not a member of anything. The Team folder's write mount is onto
		// the GROUP, so this is what the account's own permissions hang off.
		membershipErr = c.ensureOwnerGroupMembership(ctx, client)
	}

	switch {
	case probe.ServiceAccount && probe.OwnerGroup && membershipErr == nil:
		logger.Printf("nc storage: the %q service account and its %q group are in place", ncRecordingsOwner, ncRecordingsOwnerGroup)
		return ownerAccountOutcome{Reason: ownerAccountCreated}
	case probe.ServiceAccount:
		// Half done, and it must not read as either "all set" or "nothing
		// there". The account can store recordings today; the group is what
		// access control mounts onto, and the probe now says so, which is what
		// puts a create_group step (and only that step) in the setup plan.
		detail := ownerGroupIncompleteDetail(probe.OwnerGroup, membershipErr, refusal)
		logger.Printf("nc storage: %s", detail)
		return ownerAccountOutcome{Reason: ownerAccountGroupIncomplete, Detail: detail}
	default:
		reason, detail := ownerAccountRefusedDetail(refusal)
		probe.ServiceAccountAttempt = detail
		logger.Printf("nc storage: %s", detail)
		// Reported here as well as by the mode's own sanity gate, because that
		// gate names a different step first under access control (a missing app)
		// and, on an install nobody has chosen a mode for, is not reached at
		// all. The account is missing either way, and it is the prerequisite the
		// administrator can act on right now.
		ncAccessSubstrate.unavailable(storageStepServiceAccount, errors.New(probe.serviceAccountDetail()))
		return ownerAccountOutcome{Reason: reason, Detail: detail}
	}
}

// ownerAccountRefusedDetail turns what the attempt hit into the sentence an
// administrator reads, and says who can finish the job. It is deliberately the
// same for a password confirmation and for a 403 with no message: on these
// routes, from an ExApp, they are the same condition (D-661), and the remedy
// does not differ.
func ownerAccountRefusedDetail(refusal string) (reason, detail string) {
	switch {
	case refusal == "":
		// Nextcloud answered like a success and the account is still not there.
		// Rare, and worth saying plainly rather than dressing up as a refusal.
		return ownerAccountFailed, fmt.Sprintf(
			"Cassini tried to create the %q account on enable. Nextcloud reported no error and the account is still not there, so an administrator has to create it: open Cassini as an administrator and it offers to make it from your own browser session",
			ncRecordingsOwner)
	case passwordConfirmationRefusal(refusal):
		return ownerAccountNeedsPassword, fmt.Sprintf(
			"Cassini tried to create the %q account on enable and Nextcloud refused it (%s). User administration needs password confirmation, which an ExApp has no session to give. An administrator's browser does: open Cassini as an administrator and it offers to make the account as you",
			ncRecordingsOwner, refusal)
	default:
		return ownerAccountFailed, fmt.Sprintf(
			"Cassini tried to create the %q account on enable and the attempt failed (%s). An administrator's browser can make it instead: open Cassini as an administrator and it offers to make the account as you",
			ncRecordingsOwner, refusal)
	}
}

// ownerGroupIncompleteDetail names the half-created state exactly: which half
// is there, which is not, and what each costs.
func ownerGroupIncompleteDetail(groupExists bool, membershipErr error, refusal string) string {
	cause := refusal
	if membershipErr != nil {
		cause = membershipErr.Error()
	}
	if cause == "" {
		cause = "no reason given"
	}
	what := fmt.Sprintf("the %q group does not exist", ncRecordingsOwnerGroup)
	if groupExists {
		what = fmt.Sprintf("%q is not a member of the %q group", ncRecordingsOwner, ncRecordingsOwnerGroup)
	}
	return fmt.Sprintf(
		"the %q account exists but %s (%s). Recordings are stored and served either way, because the account owns them; the %q Team folder's write mount is onto that group, so access-controlled storage needs it. An administrator's browser can create it from Cassini, or run `occ group:add %s` and `occ group:adduser %s %s`",
		ncRecordingsOwner, what, cause, ncRecordingsMount, ncRecordingsOwnerGroup, ncRecordingsOwnerGroup, ncRecordingsOwner)
}

// passwordConfirmationRefusal reports whether a refusal is Nextcloud asking for
// the administrator's password.
//
// Both shapes count. The provisioning API answers an ExApp `HTTP 403`, the
// Group Folders routes answer `HTTP 200` with `ocs.meta.statuscode` 403, and
// ocsRefusal renders both with the code in the text — so the message is the
// reliable half and the code is the fallback.
//
// The code is read where ocsRefusal puts it and nowhere else: at the front, as
// `HTTP 403` or `OCS 403`. Matching a bare "403" anywhere in the string also
// matched it inside a body snippet, a quoted URL or a folder id, and told an
// administrator to confirm their password for a refusal that was nothing of the
// kind.
func passwordConfirmationRefusal(refusal string) bool {
	lower := strings.ToLower(strings.TrimSpace(refusal))
	if strings.Contains(lower, "password confirmation") {
		return true
	}
	for _, code := range []string{"http 403", "ocs 403"} {
		if lower == code || strings.HasPrefix(lower, code+":") {
			return true
		}
	}
	return false
}

// --- The two writes, judged on the envelope ------------------------------------

// createOCSGroup creates an ordinary group through the provisioning API.
//
// refusal is "" when the group is there as far as this call can tell — created,
// or already present — and `created` says which, for the log. Never use it for
// the virtual `everyone` group: an ordinary group of that name would silently
// reintroduce the new-account mount race the topology exists to remove
// (nc_provision.go step 1).
func (c ExAppConfig) createOCSGroup(ctx context.Context, client *http.Client, group string) (created bool, refusal string) {
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/groups"), url.Values{"groupid": {group}})
	if err != nil {
		return false, err.Error()
	}
	if ocsAlreadyExists(body) {
		return false, ""
	}
	if refusal := ocsRefusal(status, body); refusal != "" {
		return false, refusal
	}
	return true, ""
}

// createRecordingsOwner creates the service account with a password nobody
// keeps, and judges the answer on the envelope.
//
// The account is created INTO the owner group in the same call, which is the
// only way a refused membership write cannot leave a group-less account behind.
func (c ExAppConfig) createRecordingsOwner(ctx context.Context, client *http.Client) (created bool, refusal string) {
	// Minted per attempt from crypto/rand, never returned to this function's
	// caller, never logged, never written down. Nothing needs it: outbound calls
	// as the account go through AppAPI act-as, which presents no password.
	password, err := randomPassword()
	if err != nil {
		return false, fmt.Sprintf("generate service account password: %v", err)
	}
	status, body, err := c.apiPostForm(ctx, client, c.ocsURL("/cloud/users"), url.Values{
		"userid":      {ncRecordingsOwner},
		"password":    {password},
		"displayname": {ncRecordingsOwnerDisplayName},
		// "groups[]", not "groups". OCS decodes this field as a PHP array, and
		// a scalar makes Nextcloud answer a bare 400 with an empty body — so
		// the account is never created, every act-as-cassini call 401s, and
		// nothing downstream can be provisioned. Verified against a live
		// Nextcloud 32: "groups=" -> 400, "groups[]=" -> 200.
		"groups[]": {ncRecordingsOwnerGroup},
	})
	if err != nil {
		return false, err.Error()
	}
	if ocsAlreadyExists(body) {
		return false, ""
	}
	if refusal := ocsRefusal(status, body); refusal != "" {
		return false, refusal
	}
	return true, ""
}

// ocsAlreadyExists reports the one refusal that is not a failure: OCS 102, the
// provisioning API's "that is already there". The status is deliberately not
// consulted — v1 and v2 disagree about which HTTP code carries it, and the
// envelope says the same thing under both.
func ocsAlreadyExists(body []byte) bool {
	if ocsStatusCode(body) == 102 {
		return true
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "already exists") || strings.Contains(lower, "group exists")
}
