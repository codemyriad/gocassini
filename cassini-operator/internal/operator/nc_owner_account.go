package operator

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
)

const ncRecordingsOwnerDisplayName = "Cassini recordings"

func (c ExAppConfig) ensureServiceAccountOnEnable(ctx context.Context, client *http.Client, probe *ncStorageProbe, logger *log.Logger) {
	if probe.ServiceAccount {
		return
	}
	_, refusal := c.createRecordingsOwner(ctx, client)
	exists, err := c.userExists(ctx, client, ncRecordingsOwner)
	if err == nil && exists {
		probe.ServiceAccount = true
		return
	}
	probe.ServiceAccountAttempt = ownerAccountRefusedDetail(refusal)
	if logger != nil {
		logger.Printf("nc storage: %s", probe.ServiceAccountAttempt)
	}
}

func ownerAccountRefusedDetail(refusal string) string {
	if refusal == "" {
		return "Cassini could not confirm that its recordings account was created"
	}
	return fmt.Sprintf("Nextcloud refused to create the recordings account (%s); create the account in Nextcloud Users and recheck", refusal)
}

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
