package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

// Per-participant access control for Nextcloud Files recordings (D-534). After
// a meeting's .opus is delivered (D-529, webdav_upload.go), this layer freezes
// its audience by PROPPATCHing an advanced-ACL grant (groupfolders
// `nc:acl-list`) so only the meeting's Talk participants — resolved at publish
// time — may read the .opus. The grant is static, so later room churn does not
// change it: access is frozen at publish and editable afterwards through
// Nextcloud's own UI/occ.
//
// This requires the recordings root to be a groupfolder with advanced ACL and a
// default-deny floor, with the operator's owner delegated as its ACL manager —
// a one-time setup documented in docs/exapp-nextcloud-recordings-permissions.md.
// The whole layer is nil unless AppAPI is active.

const (
	// Nextcloud permission bits (OCP\Constants): READ=1, UPDATE=2, CREATE=4,
	// DELETE=8, SHARE=16. A read-only grant governs all bits (mask=31) and
	// allows only READ (perms=1) — explicit read-only regardless of the
	// folder's default permission.
	aclMaskAll  = 31
	aclPermRead = 1

	ncFilesACLTimeout   = 60 * time.Second
	ncFilesACLMediaType = "application/xml; charset=utf-8"

	catalogSchemaVersion = "cassini.viewer.catalog.v1"
	// emptyCatalogJSON is the fail-closed body: a valid, empty catalog. Served
	// when access control is on but the caller's meetings cannot be resolved,
	// so the viewer degrades to "no meetings" rather than leaking or erroring.
	emptyCatalogJSON = `{"version":"` + catalogSchemaVersion + `","meetings":[]}`
)

// ncFilesAccessApplier writes the advanced-ACL grants for one just-published
// meeting, acting as the recordings owner (the delegated ACL manager). Nil
// unless AppAPI is active.
type ncFilesAccessApplier func(ctx context.Context, jobID string, mappings []aclMapping, public bool) error

// ncFilesAccessApplier returns the closure, or nil when AppAPI is inactive —
// i.e. a standalone operator, which has no Nextcloud to write ACLs to. Inside
// an ExApp there is no opt-out: per-participant access is the only model
// (D-554).
func (c ExAppConfig) ncFilesAccessApplier(_ *log.Logger) ncFilesAccessApplier {
	if !c.appAPIActive() {
		return nil
	}
	client := &http.Client{Timeout: ncFilesACLTimeout}
	return func(ctx context.Context, jobID string, mappings []aclMapping, public bool) error {
		opusRel := ncRecordingsRoot + "/meetings/" + jobID + ".opus"
		if err := c.davProppatchACL(ctx, client, ncRecordingsOwner, opusRel, mappings, public); err != nil {
			return fmt.Errorf("acl opus: %w", err)
		}
		return nil
	}
}

// davProppatchACL sets the protected recording ACL on relPath: the broad
// traversal group is denied, the owner stays writable, and each participant
// gets a read-only grant. Acts as userID (the delegated ACL manager).
func (c ExAppConfig) davProppatchACL(ctx context.Context, client *http.Client, userID, relPath string, mappings []aclMapping, public bool) error {
	return c.davProppatchACLRules(ctx, client, userID, relPath, recordingACLRules(mappings, public))
}

func (c ExAppConfig) davProppatchACLRules(ctx context.Context, client *http.Client, userID, relPath string, rules []aclRule) error {
	body := aclRulesXML(rules)
	req, err := http.NewRequestWithContext(ctx, "PROPPATCH", c.davFileURL(userID, relPath), bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(body))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PROPPATCH %s -> %d", relPath, resp.StatusCode)
	}
	// A PROPPATCH returns 207 Multi-Status, and the per-property status inside
	// it is where a REJECTED property lands — the 207 itself only says the
	// request was understood. Not reading it is one of the ways a recording can
	// be written with no ACL at all while every response looks fine: outside a
	// group folder with advanced ACL, `nc:acl-list` is not a settable property,
	// so Nextcloud answers 207 with a 403 propstat and the caller sees success
	// (D-585).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("PROPPATCH %s: read multistatus: %w", relPath, err)
	}
	if status, ok := failedPropstatStatus(raw); ok {
		return fmt.Errorf("PROPPATCH %s: nc:acl-list rejected (%s) — is %s inside the %q Team folder, with advanced ACL enabled?", relPath, status, relPath, ncRecordingsMount)
	}
	return nil
}

// failedPropstatStatus reports the first non-2xx per-property status line in a
// PROPPATCH multistatus, if any. An absent or unparseable body is NOT treated as
// a failure: a bare 200 with no body is a legitimate success shape, and turning
// "I could not read the response" into "the ACL was rejected" would fail
// publishes on a technicality.
func failedPropstatStatus(raw []byte) (string, bool) {
	var ms struct {
		Responses []struct {
			Propstat []struct {
				Status string `xml:"status"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(raw, &ms); err != nil {
		return "", false
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstat {
			// "HTTP/1.1 403 Forbidden" -> the class digit is the one after the
			// space following the protocol token.
			fields := strings.Fields(ps.Status)
			if len(fields) < 2 {
				continue
			}
			if !strings.HasPrefix(fields[1], "2") {
				return strings.TrimSpace(ps.Status), true
			}
		}
	}
	return "", false
}

type aclRule struct {
	Type        string
	ID          string
	Mask        int
	Permissions int
}

// recordingACLRules denies the virtual all-users group at the file itself, then
// grants read to the Talk participants. Group folders merges rules at one path
// with allow overriding deny, so a participant who is also in `everyone` can
// read while every non-participant remains denied.
//
// The default is participant-private: `everyone` supplies every account's
// read-only mount and container traversal, but is explicitly DENIED at the leaf.
// A public conversation inverts that one rule and grants `everyone` read. The
// route remains USER-gated and creates no anonymous public link (D-552).
func recordingACLRules(mappings []aclMapping, public bool) []aclRule {
	everyonePermissions := 0
	if public {
		everyonePermissions = aclPermRead
	}
	rules := []aclRule{
		{
			Type: "group", ID: ncRecordingsEveryoneGroup,
			Mask: aclMaskAll, Permissions: everyonePermissions,
		},
		{
			// The owner needs all permissions so later archive synchronization
			// can overwrite an existing file as well as manage its ACL.
			Type: "user", ID: ncRecordingsOwner,
			Mask: aclMaskAll, Permissions: aclMaskAll,
		},
	}
	indices := map[string]int{
		"group\x00" + ncRecordingsEveryoneGroup: 0,
		"user\x00" + ncRecordingsOwner:          1,
	}
	for _, mapping := range mappings {
		key := mapping.Type + "\x00" + mapping.ID
		if i, ok := indices[key]; ok {
			rules[i].Permissions |= aclPermRead
			continue
		}
		indices[key] = len(rules)
		rules = append(rules, aclRule{
			Type: mapping.Type, ID: mapping.ID,
			Mask: aclMaskAll, Permissions: aclPermRead,
		})
	}
	return rules
}

// catalogProtectionACLRules keeps the full Files catalog private to the owner.
// Viewer requests never need direct access to it: the operator reads it as the
// owner and returns a per-caller filtered catalog.
func catalogProtectionACLRules() []aclRule {
	return []aclRule{
		{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: 0},
		{Type: "user", ID: ncRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll},
	}
}

// aclListXML builds the recording `nc:acl-list` PROPPATCH body emitted by the
// groupfolders UI (src/services/acl.ts).
func aclListXML(mappings []aclMapping, public bool) []byte {
	return aclRulesXML(recordingACLRules(mappings, public))
}

func aclRulesXML(rules []aclRule) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<d:propertyupdate xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:set><d:prop><nc:acl-list>`)
	for _, rule := range rules {
		b.WriteString(`<nc:acl><nc:acl-mapping-type>`)
		xmlEscape(&b, rule.Type)
		b.WriteString(`</nc:acl-mapping-type><nc:acl-mapping-id>`)
		xmlEscape(&b, rule.ID)
		b.WriteString(`</nc:acl-mapping-id><nc:acl-mask>`)
		fmt.Fprintf(&b, "%d", rule.Mask)
		b.WriteString(`</nc:acl-mask><nc:acl-permissions>`)
		fmt.Fprintf(&b, "%d", rule.Permissions)
		b.WriteString(`</nc:acl-permissions></nc:acl>`)
	}
	b.WriteString(`</nc:acl-list></d:prop></d:set></d:propertyupdate>`)
	return b.Bytes()
}

func xmlEscape(b *bytes.Buffer, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

// applyNCFilesAccess freezes a just-published meeting's audience to its Talk
// participants. Best-effort and non-fatal: a failure is logged but never
// changes the already-succeeded publish. No-op unless access control is enabled
// (applyNCFilesAccessFn nil) and the job is a Talk job with grantable
// participants.
// applyNCFilesAccessStrict freezes the meeting's audience and reports failure,
// so the nextcloud-files sink can fail the publish (D-549). A recording whose
// audience could not be written is not a successfully published recording.
//
// Two exits stay non-fatal, because neither means "the write failed":
//
//   - a non-Talk job has no room to derive an audience from;
//   - a room whose attendees are all guests/federated has no local principal to
//     grant, so the meeting stays owner-only. Fail-closed, logged, not an error.
//
// A lookup that errors, or an ACL write that errors, IS fatal. That does couple
// every publish to a live Talk OCS call; the retry/fallback that softens it is
// D-553.
func (rt *Runtime) applyNCFilesAccessStrict(ctx context.Context, jobID string) error {
	if rt.applyNCFilesAccessFn == nil {
		return nil
	}
	binding, ok := rt.talkBindingForJob(jobID)
	if !ok {
		rt.logger.Printf("nc files access: skip id=%s (non-Talk job / no room binding)", jobID)
		return nil
	}
	if rt.fetchTalkParticipants == nil {
		return nil
	}
	mappings, source, err := rt.resolveRecordingAudience(ctx, jobID, binding.Owner, binding.RoomToken)
	if err != nil {
		return fmt.Errorf("participants lookup: %w", err)
	}
	// A public room still has an audience even with nothing to grant per
	// participant — the `everyone` grant IS the audience — so the empty-set
	// exit must not swallow it. That ordering matters more than it looks: a
	// public room is the one most likely to be full of link guests, who yield
	// no grantable principal at all, so the old early return made the most
	// public kind of meeting produce the most private recording (D-552).
	if len(mappings) == 0 && !binding.Public {
		// A real answer, not a failure: the room's attendees are all guests,
		// email or federated, so there is no local principal to grant. The
		// recording stays readable by the owner alone — fail-closed, and the
		// only outcome here that is invisible to the operator, since the
		// publish still succeeds. See followups.
		rt.logger.Printf("nc files access: no grantable participants id=%s (guests/federated only) — meeting stays manager-only", jobID)
		return nil
	}
	if err := rt.applyNCFilesAccessFn(ctx, jobID, mappings, binding.Public); err != nil {
		return err
	}
	rt.logger.Printf("nc files access ok id=%s grants=%d source=%s public=%t root=%s", jobID, len(mappings), source, binding.Public, ncRecordingsRoot)
	return nil
}

// catalogResolveOutcome names the ways resolving a caller's readable meetings
// can end.
//
// It exists because the two readers of that resolution answer the same
// conditions DIFFERENTLY. `catalog.json` keeps its long-standing
// empty-on-failure behaviour: three shipped clients poll it, and changing what
// an empty answer means there would reach viewers and CLIs that are not
// upgraded in lockstep. `published/meetings` is loud instead, because an agent
// that reads "Nextcloud is unreachable" as "you have no meetings" acts on a
// false negative it cannot detect.
//
// The resolution itself is shared, so there is exactly ONE place that decides
// what a caller may read. A second reader must never mean a second
// access-control path.
type catalogResolveOutcome int

const (
	// catalogResolveOK: the intersection is trustworthy.
	catalogResolveOK catalogResolveOutcome = iota
	// catalogResolveNoArchive: the owner's catalog is absent. Nothing has ever
	// been published — a legitimate empty answer, not a failure.
	catalogResolveNoArchive
	// catalogResolveUnavailable: the authoritative catalog could not be read.
	catalogResolveUnavailable
	// catalogResolveScanFailed: the per-caller PROPFIND errored, so WHICH
	// meetings this caller may read is unknown — not known to be none.
	catalogResolveScanFailed
	// catalogResolveNoMount: the caller has no recordings mount at all. Every
	// account should have one through its virtual `everyone` membership before
	// its filesystem is first set up, so this means the Everyone Group app or
	// the Team-folder mapping is unavailable — substrate, not permissions.
	catalogResolveNoMount
)

// resolvedCatalog is a caller's readable slice of the archive.
type resolvedCatalog struct {
	// raw is the authoritative catalog exactly as the owner stores it. Kept so
	// a caller answering empty can still mirror its top-level shape rather than
	// inventing one.
	raw []byte
	// body is raw filtered to the meetings this caller may read. On ANY
	// non-OK outcome it is an empty catalog, so a reader that ignores the
	// outcome still cannot serve more than it should — the fail-closed property
	// is in the value, not in the discipline of the caller.
	body []byte
}

// resolveCatalogForCaller fetches the authoritative catalog as the owner
// (metadata source) and intersects it with the meetings the caller can actually
// see, enumerated by a per-caller PROPFIND scan of meetings/ (advanced-ACL
// deny-read hides the rest).
//
// The scan is re-run on every call and never memoised. That is what makes
// revocation, group changes, publicness and deletion propagate with no index
// maintenance anywhere — and it is why a cache here would be unsound in the
// permissive direction: Nextcloud gives Cassini no permission-change signal.
func (c ExAppConfig) resolveCatalogForCaller(ctx context.Context, client *http.Client, caller string, logger *log.Logger) (resolvedCatalog, catalogResolveOutcome) {
	empty := resolvedCatalog{raw: []byte(emptyCatalogJSON), body: []byte(emptyCatalogJSON)}

	raw, status, err := c.davGetBytes(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/catalog.json")
	if err != nil {
		if logger != nil {
			logger.Printf("nc files read: authoritative catalog fetch failed: %v", err)
		}
		return empty, catalogResolveUnavailable
	}
	// davGetBytes returns a nil error for a 404, so branch on STATUS, never on
	// err alone (the discipline nc_backfill.go already follows). Reading the
	// absent-archive case off err would fail open into an empty answer.
	if status == http.StatusNotFound {
		return empty, catalogResolveNoArchive
	}
	if status < 200 || status >= 300 {
		if logger != nil {
			logger.Printf("nc files read: authoritative catalog -> %d", status)
		}
		return empty, catalogResolveUnavailable
	}
	// From here the archive shape is known, so an empty answer can mirror it.
	resolved := resolvedCatalog{raw: raw, body: emptyLike(raw)}

	names, mounted, perr := c.davPropfindNames(ctx, client, caller, ncRecordingsRoot+"/meetings")
	if perr != nil {
		if logger != nil {
			logger.Printf("nc files read: per-caller scan failed caller=%s: %v — serving empty (fail closed)", caller, perr)
		}
		return resolved, catalogResolveScanFailed
	}
	if !mounted {
		if logger != nil {
			logger.Printf("nc files read: caller=%s has no recordings mount through required group %q — serving empty (fail closed)", caller, ncRecordingsEveryoneGroup)
		}
		return resolved, catalogResolveNoMount
	}
	visible := make(map[string]bool, len(names))
	for _, n := range names {
		visible[n] = true
	}
	body, ferr := filterCatalog(raw, func(base string) bool { return visible[base] })
	if ferr != nil {
		if logger != nil {
			logger.Printf("nc files read: filter catalog failed caller=%s: %v — serving empty", caller, ferr)
		}
		return resolved, catalogResolveScanFailed
	}
	resolved.body = body
	return resolved, catalogResolveOK
}

// serveFilteredCatalog writes the caller a catalog containing only the meetings
// they may read (D-534 read side). Fails CLOSED: any scan error yields an empty
// catalog, never the unfiltered one.
//
// Its status mapping is deliberately UNCHANGED from before the resolution was
// extracted: loud only when the authoritative catalog itself could not be read,
// and a valid empty catalog for every other failure. That ambiguity is a known
// wart — an empty answer here means both "you may read nothing" and "the
// substrate is mis-provisioned" — but it is one three shipped clients already
// live with, and narrowing it is its own change with its own blast radius.
// `published/meetings` is where the loud version lives.
func (c ExAppConfig) serveFilteredCatalog(ctx context.Context, w http.ResponseWriter, client *http.Client, caller string, logger *log.Logger) {
	resolved, outcome := c.resolveCatalogForCaller(ctx, client, caller, logger)
	if outcome == catalogResolveUnavailable {
		http.Error(w, "Nextcloud Files unavailable", http.StatusBadGateway)
		return
	}
	writeCatalogJSON(w, resolved.body)
}

func writeCatalogJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(ncFilesSourceHeader, ncFilesSourceValue)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// emptyLike returns the catalog with an empty meetings list, preserving the
// version/top-level shape of raw; falls back to the constant empty catalog.
func emptyLike(raw []byte) []byte {
	if body, err := filterCatalog(raw, func(string) bool { return false }); err == nil {
		return body
	}
	return []byte(emptyCatalogJSON)
}

// filterCatalog keeps only the meetings whose .opus basename passes keep,
// preserving every other field of the catalog and of each kept entry (it
// filters raw messages, so unknown fields survive).
func filterCatalog(raw []byte, keep func(opusBase string) bool) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	var meetings []json.RawMessage
	if m, ok := top["meetings"]; ok {
		if err := json.Unmarshal(m, &meetings); err != nil {
			return nil, err
		}
	}
	kept := make([]json.RawMessage, 0, len(meetings))
	for _, mr := range meetings {
		var e struct {
			AudioPath    string `json:"audioPath"`
			ArtifactPath string `json:"artifactPath"`
		}
		_ = json.Unmarshal(mr, &e)
		ref := e.AudioPath
		if ref == "" {
			ref = e.ArtifactPath
		}
		if keep(path.Base(ref)) {
			kept = append(kept, mr)
		}
	}
	mj, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	top["meetings"] = mj
	return json.Marshal(top)
}

// selfHealLeafProtection makes sure every recording under meetings/ carries an
// explicit rule for the virtual `everyone` group. It is the safety net for the
// inherited container read grant: a leaf with no broad-group rule is repaired as
// private, while a legacy recording-viewers rule is translated with its polarity
// intact (deny remains private; read remains public). Existing participant rules
// are preserved. ACLs created by the previous admin owner are also normalized
// to the dedicated `cassini` owner without changing public/private polarity. An
// error is returned so callers never expose a partially migrated tree through
// the broad root grant.
func (c ExAppConfig) selfHealLeafProtection(ctx context.Context, client *http.Client, logger *log.Logger) error {
	acls, err := c.davPropfindACLLists(ctx, client, ncRecordingsOwner, ncRecordingsRoot+"/meetings")
	if err != nil {
		if logger != nil {
			logger.Printf("nc files access: self-heal scan failed: %v", err)
		}
		return err
	}
	for base, rules := range acls {
		if !strings.HasSuffix(base, ".opus") {
			continue
		}
		next, migrated := migrateLegacyAudienceRule(rules)
		ownerNormalized := false
		if !migrated {
			switch {
			case hasExplicitEveryoneGroupRule(rules) && needsOwnerNormalization(rules):
				next = normalizeOwnerRules(rules)
				ownerNormalized = true
			case hasExplicitEveryoneGroupRule(rules):
				continue
			default:
				next = ensureProtectedRules(rules)
			}
		}
		relPath := ncRecordingsRoot + "/meetings/" + base
		if err := c.davProppatchACLRules(ctx, client, ncRecordingsOwner, relPath, next); err != nil {
			if logger != nil {
				logger.Printf("nc files access: self-heal %s failed: %v", base, err)
			}
			return fmt.Errorf("protect %s: %w", base, err)
		}
		if logger != nil {
			switch {
			case migrated:
				logger.Printf("nc files access: migrated recording %s ACL from %q to %q and owner to %q", base, ncLegacyRecordingsViewerGroup, ncRecordingsEveryoneGroup, ncRecordingsOwner)
			case ownerNormalized:
				logger.Printf("nc files access: migrated recording %s owner ACL to %q", base, ncRecordingsOwner)
			default:
				logger.Printf("nc files access: self-healed unprotected recording %s (applied everyone deny)", base)
			}
		}
	}
	return nil
}

// hasEveryoneGroupDeny reports whether the recording is participant-private.
func hasEveryoneGroupDeny(rules []aclRule) bool {
	for _, r := range rules {
		if r.Type == "group" && r.ID == ncRecordingsEveryoneGroup && r.Permissions&aclPermRead == 0 {
			return true
		}
	}
	return false
}

// hasExplicitEveryoneGroupRule reports whether the leaf states its own broad
// audience rule, either a private deny or the deliberate public read allow.
func hasExplicitEveryoneGroupRule(rules []aclRule) bool {
	for _, r := range rules {
		if r.Type == "group" && r.ID == ncRecordingsEveryoneGroup {
			return true
		}
	}
	return false
}

// everyoneRuleGovernsRead reports whether the leaf's broad-group rule actually
// decides the read bit — i.e. whether the recording's visibility is stated by
// the leaf rather than inherited from the container.
//
// The mask is the part that is easy to miss. Group Folders applies a rule as
// `perms & ^mask | (rulePerms & mask)`, so a bit the mask does not cover is
// left inherited: an `everyone` row whose mask omits READ neither grants nor
// denies anything, and the leaf still picks up the container's `everyone: READ`.
// The Advanced-permissions dialog produces exactly that when someone flips a
// recording's `everyone` toggle back to "inherit" — which the operator docs
// invite them to do.
//
// So this, not hasExplicitEveryoneGroupRule, is what the delivery gate has to
// ask. "There is a rule" and "there is a rule that does anything" are different
// claims, and only the second one means the recording is not world-readable.
func everyoneRuleGovernsRead(rules []aclRule) bool {
	for _, r := range rules {
		if r.Type == "group" && r.ID == ncRecordingsEveryoneGroup && r.Mask&aclPermRead != 0 {
			return true
		}
	}
	return false
}

// audienceApplied reports whether a leaf's rules go beyond the owner-only
// baseline that recordingACLRules(nil, false) writes at create time — that is,
// whether the meeting's audience was ever actually frozen onto it.
//
// The distinction is load-bearing, and it is why hasExplicitEveryoneGroupRule is
// not enough on its own: the baseline ALREADY states an `everyone` row (a deny),
// so "does this leaf have an ACL?" cannot tell "protected and finished" from
// "protected, but the audience write never landed". Treating the second as
// finished would make a publish that died between the content PUT and the
// audience PROPPATCH permanently invisible to its own participants — every later
// republish would see a rule set it considered healthy and skip the audience
// forever.
//
// A meeting counts as audience-applied when it either grants the broad group
// read — a public conversation's `everyone` allow IS its audience (D-552) — or
// names at least one principal besides the two the baseline writes. An admin's
// hand-added grant counts, which is deliberate: it means a republish leaves a
// manually widened recording alone.
func audienceApplied(rules []aclRule) bool {
	for _, r := range rules {
		switch {
		case r.Type == "group" && r.ID == ncRecordingsEveryoneGroup:
			if r.Permissions&aclPermRead != 0 {
				return true
			}
		case r.Type == "user" && r.ID == ncRecordingsOwner:
			// The baseline's own owner row.
		default:
			return true
		}
	}
	return false
}

// ncLeafState is what one PROPFIND tells us about a delivered recording: whether
// it is there at all, how many bytes Nextcloud thinks it holds, and the ACL rows
// bound to it.
type ncLeafState struct {
	Exists bool
	Size   int64
	Rules  []aclRule
}

// davPropfindLeafState reads one leaf's length and ACL rules in a single Depth-0
// PROPFIND. It is both the health gate a re-delivery branches on and the
// post-condition an upload is verified against.
//
// A 404 is not an error — "the recording is not there yet" is the ordinary state
// of a first publish, and the caller distinguishes it via Exists.
func (c ExAppConfig) davPropfindLeafState(ctx context.Context, client *http.Client, userID, relPath string) (ncLeafState, error) {
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns">` +
		`<d:prop><d:getcontentlength/><nc:acl-list/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relPath), bytes.NewReader(reqBody))
	if err != nil {
		return ncLeafState{}, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return ncLeafState{}, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return ncLeafState{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ncLeafState{}, fmt.Errorf("PROPFIND %s -> %d", relPath, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ncLeafState{}, err
	}
	var ms struct {
		Responses []struct {
			Propstat []struct {
				Length string `xml:"prop>getcontentlength"`
				ACLs   []struct {
					Type        string `xml:"acl-mapping-type"`
					ID          string `xml:"acl-mapping-id"`
					Mask        int    `xml:"acl-mask"`
					Permissions int    `xml:"acl-permissions"`
				} `xml:"prop>acl-list>acl"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return ncLeafState{}, fmt.Errorf("parse leaf multistatus for %s: %w", relPath, err)
	}
	if len(ms.Responses) == 0 {
		return ncLeafState{}, fmt.Errorf("PROPFIND %s: multistatus named no resource", relPath)
	}
	state := ncLeafState{Exists: true}
	// A property the resource does not carry comes back in its own 404 propstat
	// with an empty value, so both fields are gathered across every propstat and
	// an unparseable or absent length simply leaves Size at zero.
	for _, ps := range ms.Responses[0].Propstat {
		if trimmed := strings.TrimSpace(ps.Length); trimmed != "" {
			if n, convErr := strconv.ParseInt(trimmed, 10, 64); convErr == nil {
				state.Size = n
			}
		}
		for _, a := range ps.ACLs {
			state.Rules = append(state.Rules, aclRule{Type: a.Type, ID: a.ID, Mask: a.Mask, Permissions: a.Permissions})
		}
	}
	return state, nil
}

// migrateLegacyAudienceRule translates the old static broad-group principal to
// `everyone`, preserving public/read versus private/deny and all participant
// grants. The owner rule is normalized while the file is being rewritten.
func migrateLegacyAudienceRule(existing []aclRule) ([]aclRule, bool) {
	haveLegacy := false
	permissions := 0
	out := make([]aclRule, 0, len(existing)+1)
	for _, r := range existing {
		if r.Type == "group" && r.ID == ncLegacyRecordingsViewerGroup {
			haveLegacy = true
			permissions |= r.Permissions & aclPermRead
			continue
		}
		out = append(out, r)
	}
	if !haveLegacy {
		return existing, false
	}
	audience := aclRule{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: permissions}
	return normalizeOwnerRules(append([]aclRule{audience}, out...)), true
}

// needsOwnerNormalization finds ACLs created before the dedicated service
// account: they either lack the cassini owner or retain the old admin owner.
func needsOwnerNormalization(rules []aclRule) bool {
	haveOwner := false
	for _, r := range rules {
		if r.Type == "user" && r.ID == ncRecordingsOwner && r.Permissions == aclMaskAll {
			haveOwner = true
		}
		if isLegacyOwnerRule(r) {
			return true
		}
	}
	return !haveOwner
}

func isLegacyOwnerRule(r aclRule) bool {
	return r.Type == "user" && r.ID == ncLegacyRecordingsOwner &&
		ncLegacyRecordingsOwner != ncRecordingsOwner && r.Permissions == aclMaskAll
}

// normalizeOwnerRules removes the generated legacy admin-owner rule, preserves
// participant grants (including a manually narrowed admin read), and ensures the
// dedicated service account is the sole generated ALL-permission owner.
func normalizeOwnerRules(existing []aclRule) []aclRule {
	haveOwner := false
	out := make([]aclRule, 0, len(existing)+1)
	for _, r := range existing {
		switch {
		case isLegacyOwnerRule(r):
			continue
		case r.Type == "user" && r.ID == ncRecordingsOwner:
			r.Mask, r.Permissions = aclMaskAll, aclMaskAll
			haveOwner = true
		}
		out = append(out, r)
	}
	if !haveOwner {
		out = append(out, aclRule{Type: "user", ID: ncRecordingsOwner, Mask: aclMaskAll, Permissions: aclMaskAll})
	}
	return out
}

// ensureProtectedRules returns rules with an `everyone` deny and owner ALL,
// preserving participant grants. Legacy/current broad-group rules are replaced
// rather than duplicated.
func ensureProtectedRules(existing []aclRule) []aclRule {
	out := make([]aclRule, 0, len(existing)+2)
	for _, r := range existing {
		if r.Type == "group" && (r.ID == ncRecordingsEveryoneGroup || r.ID == ncLegacyRecordingsViewerGroup) {
			continue
		}
		out = append(out, r)
	}
	deny := aclRule{Type: "group", ID: ncRecordingsEveryoneGroup, Mask: aclMaskAll, Permissions: 0}
	return normalizeOwnerRules(append([]aclRule{deny}, out...))
}

// davPropfindACLLists lists relDir (Depth 1) requesting each child's nc:acl-list
// and returns the parsed rules keyed by child basename.
func (c ExAppConfig) davPropfindACLLists(ctx context.Context, client *http.Client, userID, relDir string) (map[string][]aclRule, error) {
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:" xmlns:nc="http://nextcloud.org/ns"><d:prop><nc:acl-list/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relDir), bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("PROPFIND %s -> %d", relDir, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var ms struct {
		Responses []struct {
			Href     string `xml:"href"`
			Propstat []struct {
				ACLs []struct {
					Type        string `xml:"acl-mapping-type"`
					ID          string `xml:"acl-mapping-id"`
					Mask        int    `xml:"acl-mask"`
					Permissions int    `xml:"acl-permissions"`
				} `xml:"prop>acl-list>acl"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, fmt.Errorf("parse acl multistatus: %w", err)
	}
	out := make(map[string][]aclRule, len(ms.Responses))
	for _, r := range ms.Responses {
		base := path.Base(strings.TrimRight(r.Href, "/"))
		if decoded, derr := url.PathUnescape(base); derr == nil {
			base = decoded
		}
		var rules []aclRule
		for _, ps := range r.Propstat {
			for _, a := range ps.ACLs {
				rules = append(rules, aclRule{Type: a.Type, ID: a.ID, Mask: a.Mask, Permissions: a.Permissions})
			}
		}
		out[base] = rules
	}
	return out, nil
}

func (c ExAppConfig) davGetBytes(ctx context.Context, client *http.Client, userID, relPath string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.davFileURL(userID, relPath), nil)
	if err != nil {
		return nil, 0, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer drainClose(resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// davPropfindNames lists the immediate children of relDir as userID (Depth: 1)
// and returns the .opus basenames the user can see. Advanced-ACL deny-read
// hides paths the user lacks read on, so the result is naturally access-scoped.
//
// visible reports whether the collection itself was reachable. A 404 means the
// caller has no mount of the recordings group folder at all — a different
// condition from "mounted, but every recording is denied", and one the caller
// can act on (see serveFilteredCatalog). Both yield an empty list; only the
// second is a legitimate empty answer.
func (c ExAppConfig) davPropfindNames(ctx context.Context, client *http.Client, userID, relDir string) (names []string, visible bool, err error) {
	// Request only <d:resourcetype/>: the href (all we need) is always returned,
	// and a minimal prop set keeps each child's response element to a few
	// hundred bytes instead of the multi-KiB allprops default.
	reqBody := []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", c.davFileURL(userID, relDir), bytes.NewReader(reqBody))
	if err != nil {
		return nil, false, err
	}
	c.setAppAPIDAVHeadersForUser(req, userID)
	req.Header.Set("Depth", "1")
	req.Header.Set("Content-Type", ncFilesACLMediaType)
	req.ContentLength = int64(len(reqBody))
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("PROPFIND %s -> %d", relDir, resp.StatusCode)
	}
	// Read with an explicit cap and detect truncation: a silently truncated
	// multistatus would fail to parse and blank the listing for every caller
	// (fail-closed but self-inflicted), so surface it loudly instead.
	const maxMultistatus = 64 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMultistatus+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxMultistatus {
		return nil, false, fmt.Errorf("PROPFIND %s: multistatus exceeds %d bytes (too many meetings?)", relDir, maxMultistatus)
	}
	var ms struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(body, &ms); err != nil {
		return nil, false, fmt.Errorf("parse multistatus: %w", err)
	}
	out := make([]string, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		href := strings.TrimRight(r.Href, "/")
		base := path.Base(href)
		if decoded, derr := url.PathUnescape(base); derr == nil {
			base = decoded
		}
		if strings.HasSuffix(base, ".opus") {
			out = append(out, base)
		}
	}
	return out, true, nil
}
