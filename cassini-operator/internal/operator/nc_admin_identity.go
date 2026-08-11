package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// Resolving the administrator Cassini performs privileged setup as (D-585
// outcome 3).
//
// The old implementation was circular. It discovered the administrator with
// `GET /cloud/groups/admin` — a request signed as `c.provisioningUser()`, which
// before resolution is the literal fallback `admin`, i.e. the very id it was
// trying to discover. On an instance whose administrator is named anything else
// that request is made as an account that does not exist, 401s, falls back to
// `admin` anyway, and every subsequent OCS and Group Folders call is rejected.
// The zero-members branch logged nothing at all.
//
// Naming an administrator is information an ExApp does not have, and every core
// OCS route that would reveal one is itself admin-gated — so discovery has to be
// a PROBE. What makes it more than guessing is that the candidate list can be
// read from the instance rather than invented:
//
//	ENUMERATE (presupposes nothing)        PROBE (admin-gated, authoritative)
//	──────────────────────────────         ──────────────────────────────────
//	GET apps/app_api/api/v1/users          GET cloud/groups/admin AS <candidate>
//	  as "" (app-scoped)  ->  200            2xx  this candidate IS an admin,
//	  ["admin","alice","cassini",...]             and the body is the roster
//	                                         403  exists, is not an admin -> next
//	                                         401  no such act-as          -> next
//	                                         404  the provisioning route is
//	                                              missing -> abort, degraded
//
// Verified against a live Nextcloud 34 / AppAPI 34.0.0 (see
// _ivans-notes/development/549-install-substrate-preflight/spike-x1-*.md):
// the AppAPI user route answers 200 with the empty user id while every
// `/cloud/...` route answers 401, and `cassini` — an account that exists but is
// deliberately not an admin — answers 403. Both discriminators are real.
//
// What this does NOT do: eliminate the presupposition entirely. The probe still
// names candidates. But they are facts read from the instance rather than
// guesses, so the only way to fail is for no account on the instance to be an
// administrator Cassini may act as — which is legible, recorded in /status, and
// overridable with CASSINI_NC_ADMIN_USER.

// adminProbeMaxCandidates caps the roster sweep. The env override and the
// conventional `admin` are probed first and cost two calls, so this only bites
// on an instance where neither is the administrator — and there a full sweep of
// a large user base would turn one enabled edge into thousands of OCS calls.
// When the cap bites it is logged, because a silent truncation would look
// exactly like "no administrator exists".
const adminProbeMaxCandidates = 24

// errAdminRouteMissing means the probe could not be completed — the
// provisioning route is absent or Nextcloud is unreachable. Distinct from "no
// candidate was an administrator", because nothing about the instance's accounts
// has been established.
var errAdminRouteMissing = errors.New("the Nextcloud provisioning API could not be reached")

// errNoAdminResolved means every candidate was probed and none was an
// administrator Cassini may act as.
var errNoAdminResolved = errors.New("no Nextcloud administrator could be resolved")

// resolvedAdminRoster caches the admin-group membership discovered by a
// successful probe, so a re-run inside the same process starts from the answer.
var resolvedAdminRoster atomic.Pointer[[]string]

// accountRoster lists every account on the instance, acting app-scoped (empty
// user id). This is the one call in the whole flow that presupposes no identity.
// It is an AppAPI route, not core OCS, which is exactly why it answers without
// one.
func (c ExAppConfig) accountRoster(ctx context.Context, client *http.Client) ([]string, error) {
	status, body, err := c.apiGetAs(ctx, client, "", c.ocsURL("/apps/app_api/api/v1/users"))
	if err != nil {
		return nil, err
	}
	if status/100 != 2 {
		return nil, fmt.Errorf("GET app_api users -> %d: %s", status, snippet(body))
	}
	var payload struct {
		OCS struct {
			Data []string `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode app_api users: %w", err)
	}
	return payload.OCS.Data, nil
}

// adminCandidates builds the ordered, deduplicated probe list.
//
// The explicit override comes first because an operator who set it has already
// answered the question. `admin` comes second because it is the conventional id
// and costs one call on the overwhelming majority of instances. The roster comes
// last: it is what makes a non-`admin` instance work at all, and it is the only
// part that needs a second HTTP call to build.
func (c ExAppConfig) adminCandidates(ctx context.Context, client *http.Client, logger *log.Logger) []string {
	var ordered []string
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		ordered = append(ordered, name)
	}

	add(os.Getenv(envNCAdminUser))
	if cached := resolvedAdminRoster.Load(); cached != nil {
		for _, name := range *cached {
			add(name)
		}
	}
	add(defaultNextcloudAdminUser)

	roster, err := c.accountRoster(ctx, client)
	if err != nil {
		// Not fatal: the override and the conventional id are still worth
		// probing, and on most instances one of them is the answer. Logged
		// because it silently narrows the search.
		if logger != nil {
			logger.Printf("nc provision: could not enumerate accounts (%v); probing only %v", err, ordered)
		}
		return ordered
	}
	for _, name := range roster {
		if len(ordered) >= adminProbeMaxCandidates {
			if logger != nil {
				logger.Printf("nc provision: %d accounts on this instance; probing only the first %d for administrator rights — set %s to name one directly", len(roster), adminProbeMaxCandidates, envNCAdminUser)
			}
			break
		}
		add(name)
	}
	return ordered
}

// resolveAdminIdentity probes candidates until one turns out to be an
// administrator, caches it and the admin roster it revealed, and returns it.
//
// It reads the RAW status rather than going through groupMembers, which maps 404
// to (nil, nil) and would erase exactly the signal this switch needs.
func (c ExAppConfig) resolveAdminIdentity(ctx context.Context, client *http.Client, logger *log.Logger) (string, error) {
	candidates := c.adminCandidates(ctx, client, logger)
	if len(candidates) == 0 {
		return "", errNoAdminResolved
	}
	for _, candidate := range candidates {
		status, body, err := c.apiGetAs(ctx, client, candidate, withFormatJSON(c.ocsURL("/cloud/groups/admin")))
		switch {
		case err != nil:
			return "", fmt.Errorf("%w: acting as %q: %v", errAdminRouteMissing, candidate, err)
		case status/100 == 2:
			admins, perr := parseOCSUserList(body)
			if perr == nil && len(admins) > 0 {
				stored := append([]string(nil), admins...)
				resolvedAdminRoster.Store(&stored)
			}
			var boxed any = candidate
			resolvedProvisioningUser.Store(&boxed)
			if logger != nil {
				logger.Printf("nc provision: acting as administrator %q", candidate)
			}
			return candidate, nil
		case status == http.StatusNotFound:
			// The route itself is absent — an instance fault, not an answer
			// about this candidate. Probing further would learn nothing.
			return "", fmt.Errorf("%w: /cloud/groups/admin -> 404", errAdminRouteMissing)
		case status == http.StatusUnauthorized, status == http.StatusForbidden:
			// 401: no such account, or AppAPI refused the act-as.
			// 403: the account exists but is not an administrator.
			// Either way, try the next candidate.
			continue
		default:
			return "", fmt.Errorf("%w: acting as %q -> %d: %s", errAdminRouteMissing, candidate, status, snippet(body))
		}
	}
	return "", fmt.Errorf("%w (probed %d: %v)", errNoAdminResolved, len(candidates), candidates)
}
