package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Nextcloud sharing policies that can refuse Cassini's recording shares (D-885).
//
// Every recording reaches its audience through ordinary shares made by the
// `cassini` account, so an instance's sharing policy applies to it exactly as
// it would to a person. Some of those settings veto the shares outright, and
// none of them is visible in capabilities: the storage preflight's share-API
// GET answers regardless. They used to surface only at publication — as a
// failed job when the person who pressed Record was refused, and as meetings
// silently missing for everyone else.
//
// So they are read here, as the administrator the preflight already resolved,
// and judged against `cassini`'s own groups. The semantics are Nextcloud
// server's own (verified against its source, NC 34):
//
//   - shareapi_enabled != "yes": the Share API refuses every share.
//   - shareapi_exclude_groups: "no" (or unset) is off; "allow" lets ONLY
//     members of the listed groups share; any other value forbids members of
//     ANY listed group from sharing (ShareDisableChecker).
//   - shareapi_only_share_with_group_members = "yes": a user share needs a
//     group the two accounts have in common, after removing the groups in
//     shareapi_only_share_with_group_members_exclude_group_list — that list
//     narrows the comparison, it exempts nobody; a group share needs the
//     sharer to be in that group (RestrictInteractionListener).
//   - shareapi_allow_group_sharing != "yes": every group share is refused.
//
// An unset key reads back empty: Nextcloud's defaults are applied here, not
// by the endpoint.
const (
	ncShareAPIEnabledKey      = "shareapi_enabled"
	ncOnlyGroupMembersKey     = "shareapi_only_share_with_group_members"
	ncOnlyGroupMembersSkipKey = "shareapi_only_share_with_group_members_exclude_group_list"
	ncExcludeGroupsKey        = "shareapi_exclude_groups"
	ncExcludeGroupsListKey    = "shareapi_exclude_groups_list"
	ncAllowGroupSharingKey    = "shareapi_allow_group_sharing"
)

const (
	excludeGroupsOff       = ""
	excludeGroupsBlocklist = "yes"
	excludeGroupsAllowlist = "allow"
)

type sharingPolicySettings struct {
	APIEnabled       bool
	OnlyGroupMembers bool
	// IgnoredGroups do not count as common groups under OnlyGroupMembers.
	IgnoredGroups     []string
	ExcludeGroupsMode string
	ExcludeGroupsList []string
	AllowGroupSharing bool
}

// sharingPolicyFinding is what the Recording sharing row reports. Steps are
// instructions for Nextcloud's own settings: Cassini never changes sharing
// permissions or group memberships itself.
type sharingPolicyFinding struct {
	State   string
	Code    string
	Message string
	Steps   []readinessStep
	// ShareableGroups are the groups Cassini can share through while
	// Nextcloud restricts sharing to group members; empty when unrestricted.
	ShareableGroups []string
	CheckedAt       string
}

func (c ExAppConfig) appConfigValue(ctx context.Context, client *http.Client, actor, key string) (string, error) {
	status, body, err := c.apiGetAs(ctx, client, actor, c.ocsURL("/apps/provisioning_api/api/v1/config/apps/core/"+url.PathEscape(key)))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", key, err)
	}
	if refusal := ocsRefusal(status, body); refusal != "" {
		return "", fmt.Errorf("read %s: %s", key, refusal)
	}
	var env struct {
		OCS struct {
			Data *struct {
				Data json.RawMessage `json:"data"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("read %s: decode: %w", key, err)
	}
	if env.OCS.Data == nil {
		return "", fmt.Errorf("read %s: no value in the response", key)
	}
	raw := strings.TrimSpace(string(env.OCS.Data.Data))
	if raw == "" || raw == "null" {
		return "", nil
	}
	var value string
	if json.Unmarshal(env.OCS.Data.Data, &value) == nil {
		return value, nil
	}
	return raw, nil
}

func (c ExAppConfig) userGroups(ctx context.Context, client *http.Client, actor, userID string) ([]string, error) {
	status, body, err := c.apiGetAs(ctx, client, actor, c.ocsURL("/cloud/users/"+url.PathEscape(userID)+"/groups"))
	if err != nil {
		return nil, fmt.Errorf("read %s's groups: %w", userID, err)
	}
	if refusal := ocsRefusal(status, body); refusal != "" {
		return nil, fmt.Errorf("read %s's groups: %s", userID, refusal)
	}
	var env struct {
		OCS struct {
			Data *struct {
				Groups []string `json:"groups"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("read %s's groups: decode: %w", userID, err)
	}
	if env.OCS.Data == nil {
		return nil, fmt.Errorf("read %s's groups: no groups in the response", userID)
	}
	return cleanGroupList(env.OCS.Data.Groups), nil
}

// sharingPolicyReadTimeout bounds the reads as a whole. They run inside the
// storage preflight, which shares one deadline with the host and Talk probes;
// a slow Nextcloud must not spend that budget on settings one at a time.
const sharingPolicyReadTimeout = 10 * time.Second

var sharingPolicyKeys = []string{ncShareAPIEnabledKey, ncOnlyGroupMembersKey, ncOnlyGroupMembersSkipKey, ncExcludeGroupsKey, ncExcludeGroupsListKey, ncAllowGroupSharingKey}

func (c ExAppConfig) readSharingPolicy(ctx context.Context, client *http.Client, admin string) (sharingPolicySettings, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, sharingPolicyReadTimeout)
	defer cancel()
	values := make([]string, len(sharingPolicyKeys))
	errs := make([]error, len(sharingPolicyKeys)+1)
	var groups []string
	var wg sync.WaitGroup
	for i, key := range sharingPolicyKeys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			values[i], errs[i] = c.appConfigValue(ctx, client, admin, key)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		groups, errs[len(sharingPolicyKeys)] = c.userGroups(ctx, client, admin, ncRecordingsOwner)
	}()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return sharingPolicySettings{}, nil, err
		}
	}
	value := map[string]string{}
	for i, key := range sharingPolicyKeys {
		value[key] = values[i]
	}
	// Compared exactly, as Nextcloud does; only an empty value — which is how
	// an unset key reads back — takes Nextcloud's default.
	settings := sharingPolicySettings{
		APIEnabled:        value[ncShareAPIEnabledKey] == "" || value[ncShareAPIEnabledKey] == "yes",
		OnlyGroupMembers:  value[ncOnlyGroupMembersKey] == "yes",
		IgnoredGroups:     decodeIgnoredGroupsList(value[ncOnlyGroupMembersSkipKey]),
		ExcludeGroupsMode: excludeGroupsMode(value[ncExcludeGroupsKey]),
		ExcludeGroupsList: decodeExcludeGroupsList(value[ncExcludeGroupsListKey]),
		AllowGroupSharing: value[ncAllowGroupSharingKey] == "" || value[ncAllowGroupSharingKey] == "yes",
	}
	return settings, groups, nil
}

func excludeGroupsMode(raw string) string {
	switch raw {
	case "", "no":
		return excludeGroupsOff
	case excludeGroupsAllowlist:
		return excludeGroupsAllowlist
	default:
		return excludeGroupsBlocklist
	}
}

// decodeExcludeGroupsList reads shareapi_exclude_groups_list the way
// ShareDisableChecker does: json_decode($raw, true), and only when that is
// null, the comma-separated form older versions stored (explode, untrimmed).
func decodeExcludeGroupsList(raw string) []string {
	if raw == "" {
		return nil
	}
	if groups, ok := decodePHPGroupList(raw); ok {
		return groups
	}
	return cleanGroupList(strings.Split(raw, ","))
}

// decodeIgnoredGroupsList reads
// shareapi_only_share_with_group_members_exclude_group_list the way Nextcloud's
// share manager does: json_decode($raw, true) ?? [] — a value that is not JSON
// ignores no group.
func decodeIgnoredGroupsList(raw string) []string {
	groups, _ := decodePHPGroupList(raw)
	return groups
}

// decodePHPGroupList mirrors json_decode($raw, true) for a list of group ids.
// A JSON array yields its strings; so does a JSON object, which is what
// Nextcloud itself writes once a listed group is deleted (array_diff keeps the
// remaining keys, and json_encode then emits {"1":"robots"}). ok is false where
// json_decode would return null.
func decodePHPGroupList(raw string) ([]string, bool) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return nil, false
	}
	var groups []string
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if group, ok := item.(string); ok {
				groups = append(groups, group)
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			a, aErr := strconv.Atoi(keys[i])
			b, bErr := strconv.Atoi(keys[j])
			if aErr == nil && bErr == nil {
				return a < b
			}
			return keys[i] < keys[j]
		})
		for _, key := range keys {
			if group, ok := v[key].(string); ok {
				groups = append(groups, group)
			}
		}
	}
	return cleanGroupList(groups), true
}

// cleanGroupList drops empty and repeated ids. It does not trim: Nextcloud
// compares group ids exactly.
func cleanGroupList(groups []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, group := range groups {
		if group == "" || seen[group] {
			continue
		}
		seen[group] = true
		out = append(out, group)
	}
	return out
}

// Group ids compare exactly, as Nextcloud's array_intersect does.
func intersectGroups(a, b []string) []string {
	in := map[string]bool{}
	for _, group := range b {
		in[group] = true
	}
	out := []string{}
	for _, group := range a {
		if in[group] {
			out = append(out, group)
		}
	}
	return out
}

func subtractGroups(a, b []string) []string {
	out := []string{}
	skip := map[string]bool{}
	for _, group := range b {
		skip[group] = true
	}
	for _, group := range a {
		if !skip[group] {
			out = append(out, group)
		}
	}
	return out
}

// quotedGroups is for steps, which the panel renders with code spans.
func quotedGroups(groups []string) string {
	quoted := make([]string, len(groups))
	for i, group := range groups {
		quoted[i] = "`" + group + "`"
	}
	return strings.Join(quoted, ", ")
}

// namedGroups is for messages, which the panel renders as plain text.
func namedGroups(groups []string) string {
	named := make([]string, len(groups))
	for i, group := range groups {
		named[i] = "“" + group + "”"
	}
	return strings.Join(named, ", ")
}

type sharingPolicyIssue struct {
	state, code, sentence string
	steps                 []readinessStep
}

// evaluateSharingPolicy judges the settings against `cassini`'s groups. It
// fails only where the settings prove that no share can be made; where shares
// work for part of an audience it warns, naming what it knows, because whether
// a set of groups covers every participant is not something it can establish.
//
// Every message reads the same way: the VERDICT (who can receive recordings),
// then the CAUSE (the setting by its Nextcloud name, and that Cassini shares
// from its own account), then the CONSEQUENCE. Steps are the ways out, one
// full sentence each. The app's Doctor preview and its browser tests pin
// these texts too.
func evaluateSharingPolicy(s sharingPolicySettings, groups []string) sharingPolicyFinding {
	groups = cleanGroupList(groups)
	var issues []sharingPolicyIssue
	var shareable []string

	const (
		nobody          = "Nobody can receive recordings. "
		refusedAll      = " Every share is refused, so no new meetings can be published."
		sharingSettings = "Administration settings › Sharing"
		restrictSetting = "“Restrict users to only share with users in their groups”"
	)
	notAdmin := func(step string) string {
		// Nextcloud deletes person-to-person shares between accounts that no
		// longer share a group when one leaves it under the restriction
		// (DefaultShareProvider::userDeletedFromGroup), so the advice to join
		// a group carries the warning to stay in it.
		return step + " It does not need to be an administrator. Keep it there: removing it later makes Nextcloud remove people's access to recordings already shared through that group."
	}

	if !s.APIEnabled {
		issues = append(issues, sharingPolicyIssue{
			state: "needs_action", code: "sharing_api_disabled",
			sentence: nobody + "Nextcloud's “Allow apps to use the Share API” is off, so every share Cassini makes is refused and no new meetings can be published.",
			steps:    []readinessStep{{Label: "Turn on “Allow apps to use the Share API” under " + sharingSettings + "."}},
		})
	}

	switch s.ExcludeGroupsMode {
	case excludeGroupsBlocklist:
		if hit := intersectGroups(groups, s.ExcludeGroupsList); len(hit) > 0 {
			membership := "that group"
			if len(hit) > 1 {
				membership = "those groups"
			}
			issues = append(issues, sharingPolicyIssue{
				state: "needs_action", code: "sharing_disabled_for_account",
				sentence: fmt.Sprintf("%sNextcloud's “Exclude some groups from sharing” lists %s, and Cassini shares from its %s account, which is in %s.%s", nobody, namedGroups(hit), ncRecordingsOwner, membership, refusedAll),
				steps: []readinessStep{
					{Label: fmt.Sprintf("Remove %s from the excluded groups under %s.", quotedGroups(hit), sharingSettings)},
					{Label: fmt.Sprintf("Or take `%s` out of %s on Nextcloud's Users page.", ncRecordingsOwner, quotedGroups(hit))},
				},
			})
		}
	case excludeGroupsAllowlist:
		if len(intersectGroups(groups, s.ExcludeGroupsList)) == 0 {
			sentence := nobody + "Nextcloud's “Limit sharing to some groups” allows no group at all, so no account may share." + refusedAll
			steps := []readinessStep{{Label: fmt.Sprintf("Choose the groups allowed to share under %s, including one `%s` is in.", sharingSettings, ncRecordingsOwner)}}
			if len(s.ExcludeGroupsList) > 0 {
				sentence = fmt.Sprintf("%sNextcloud's “Limit sharing to some groups” allows only %s, and Cassini shares from its %s account, which is in none of them.%s", nobody, namedGroups(s.ExcludeGroupsList), ncRecordingsOwner, refusedAll)
				steps = []readinessStep{
					{Label: fmt.Sprintf("Add `%s` to one of %s on Nextcloud's Users page.", ncRecordingsOwner, quotedGroups(s.ExcludeGroupsList))},
					{Label: "Or allow sharing for everyone under " + sharingSettings + "."},
				}
			}
			issues = append(issues, sharingPolicyIssue{state: "needs_action", code: "sharing_disabled_for_account", sentence: sentence, steps: steps})
		}
	}

	if s.OnlyGroupMembers {
		shareable = subtractGroups(groups, s.IgnoredGroups)
		lift := readinessStep{Label: "Turn off " + restrictSetting + " under " + sharingSettings + "."}
		joinOne := readinessStep{Label: notAdmin(fmt.Sprintf("Or add `%s` to a group everyone who records is in.", ncRecordingsOwner))}
		switch {
		case len(shareable) == 0 && len(groups) == 0:
			issues = append(issues, sharingPolicyIssue{
				state: "needs_action", code: "sharing_no_common_group",
				sentence: fmt.Sprintf("%sNextcloud's %s is on, and Cassini shares from its %s account, which is in no group.%s", nobody, restrictSetting, ncRecordingsOwner, refusedAll),
				steps:    []readinessStep{lift, joinOne},
			})
		case len(shareable) == 0:
			issues = append(issues, sharingPolicyIssue{
				state: "needs_action", code: "sharing_no_common_group",
				sentence: fmt.Sprintf("%sNextcloud's %s is on, and every group the %s account is in (%s) is ignored when Nextcloud checks membership.%s", nobody, restrictSetting, ncRecordingsOwner, namedGroups(groups), refusedAll),
				steps: []readinessStep{lift, joinOne,
					{Label: fmt.Sprintf("Or stop ignoring %s under “Ignore the following groups when checking group membership”.", quotedGroups(groups))},
				},
			})
		default:
			verdict := fmt.Sprintf("Only members of %s can receive recordings.", namedGroups(shareable))
			membership := fmt.Sprintf("which is only in %s", namedGroups(shareable))
			consequence := "Anyone outside these groups cannot open the recordings of their meetings, and a recording they start is not published at all."
			if len(shareable) == 1 && shareable[0] == "admin" {
				verdict = "Only administrators can receive recordings."
				membership += ", the administrators' group"
				consequence = "Anyone who is not an administrator cannot open the recordings of their meetings, and a recording they start is not published at all."
			}
			sentence := fmt.Sprintf("%s Nextcloud's %s is on, and Cassini shares from its %s account, %s. %s", verdict, restrictSetting, ncRecordingsOwner, membership, consequence)
			joinAll := readinessStep{Label: fmt.Sprintf("Or add `%s` to every group whose members should receive recordings. A group everyone is in covers everybody; `%s` does not need to be an administrator. Keep it there: removing it later makes Nextcloud remove people's access to recordings already shared through that group.", ncRecordingsOwner, ncRecordingsOwner)}
			issues = append(issues, sharingPolicyIssue{state: "warn", code: "sharing_limited_to_groups", sentence: sentence, steps: []readinessStep{lift, joinAll}})
		}
	}

	if !s.AllowGroupSharing {
		issues = append(issues, sharingPolicyIssue{
			state: "warn", code: "group_sharing_disabled",
			sentence: "Groups in a room do not receive recordings. Nextcloud's “Allow sharing with groups” is off, so a meeting whose room includes a group is published without access for that group's members, unless they are in the room themselves.",
			steps:    []readinessStep{{Label: "Turn on “Allow sharing with groups” under " + sharingSettings + "."}},
		})
	}

	if len(issues) == 0 {
		finding := sharingPolicyFinding{State: "passed", Code: "sharing_permitted",
			Message: "Nextcloud's sharing settings let Cassini share recordings with everyone in a meeting. Whether each person has accepted the share is theirs to decide; anyone with recordings waiting sees them in Browse."}
		if s.OnlyGroupMembers {
			finding.ShareableGroups = shareable
		}
		return finding
	}
	worst := issues[0]
	var sentences []string
	var steps []readinessStep
	seen := map[string]bool{}
	for _, issue := range issues {
		if readinessStateRank[issue.state] > readinessStateRank[worst.state] {
			worst = issue
		}
		sentences = append(sentences, issue.sentence)
		for _, step := range issue.steps {
			if !seen[step.Label] {
				seen[step.Label] = true
				steps = append(steps, step)
			}
		}
	}
	return sharingPolicyFinding{
		State: worst.state, Code: worst.code,
		Message: strings.Join(sentences, " "), Steps: steps,
		ShareableGroups: shareable,
	}
}

// unreadableSharingPolicy is a check that could not establish anything. It is
// an absence, never a verdict: a read that failed says nothing about whether
// sharing works.
func unreadableSharingPolicy(err error) sharingPolicyFinding {
	// Running out of time says nothing about reachability or rights, so it
	// must not send the reader to check either.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return sharingPolicyFinding{
			State: "not_verified", Code: "sharing_policy_check_timed_out",
			Message: "Nextcloud did not answer in time when Cassini read its sharing settings, so whether they let it share recordings is not known.",
			Steps:   []readinessStep{{Label: "Check again. If it keeps running out of time, Nextcloud is answering slowly: the Recording storage check above uses the same connection"}},
		}
	}
	return sharingPolicyFinding{
		State: "not_verified", Code: "sharing_policy_unreadable",
		Message: "Cassini could not read Nextcloud's sharing settings, so whether they let it share recordings is not known.",
		Steps: []readinessStep{
			{Label: "Cassini reads them as the Nextcloud administrator it acts as. Check that Nextcloud is reachable and that account is still an administrator, then check again"},
		},
	}
}

// checkSharingPolicy records the Recording sharing finding. Never fatal to the
// storage preflight it runs in: a policy that limits sharing does not stop
// Cassini storing recordings, and admission is not this check's to decide.
func (c ExAppConfig) checkSharingPolicy(ctx context.Context, client *http.Client, admin string, logger *log.Logger) {
	settings, groups, err := c.readSharingPolicy(ctx, client, admin)
	if err != nil {
		if logger != nil {
			logger.Printf("nc sharing policy: %v", err)
		}
		ncAccessSubstrate.setSharingPolicy(unreadableSharingPolicy(err))
		return
	}
	finding := evaluateSharingPolicy(settings, groups)
	if finding.State != "passed" && logger != nil {
		logger.Printf("nc sharing policy: %s: %s", finding.Code, finding.Message)
	}
	ncAccessSubstrate.setSharingPolicy(finding)
}

// shareAPIDisabledFinding is the storage row's verdict when the storage check
// stopped at the share API and the sharing settings show that API is off.
// Only that one cause: other settings belong to the Recording sharing row,
// which speaks once storage passes.
func shareAPIDisabledFinding(access statusRecordingsAccess) (sharingPolicyFinding, bool) {
	if !access.Applicable || access.OK || access.Step != "sharing_api" {
		return sharingPolicyFinding{}, false
	}
	finding, checked := ncAccessSubstrate.sharingPolicyFinding()
	// The Share API is evaluated first and never outranked, so its code is the
	// finding's whenever it is off, whatever else is wrong.
	if !checked || finding.Code != "sharing_api_disabled" {
		return sharingPolicyFinding{}, false
	}
	return evaluateSharingPolicy(sharingPolicySettings{AllowGroupSharing: true}, nil), true
}

const sharingRowID = "storage.sharing"

// sharingPolicyRow is the Recording sharing row, from the last finding.
func sharingPolicyRow(finding sharingPolicyFinding, checked bool) readinessCheck {
	if !checked {
		return readinessCheck{ID: sharingRowID, State: "not_verified", Code: "sharing_not_checked",
			Message: "Nextcloud's sharing settings have not been checked yet.", Action: "recheck"}
	}
	row := readinessCheck{ID: sharingRowID, State: finding.State, Code: finding.Code,
		Message: finding.Message, Steps: finding.Steps, CheckedAt: finding.CheckedAt}
	if finding.State == "not_verified" {
		row.Action = "recheck"
	}
	return row
}
