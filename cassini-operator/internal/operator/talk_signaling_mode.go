package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// hpbDocsURL is Nextcloud Talk's own documentation, on its /en/stable/ path.
//
// Nextcloud's resource rather than the signaling project's, and verified when
// written: this page returns 200 and discusses the High Performance Backend at
// length. Two plausible-looking alternatives do not — /en/stable/
// standalone-signaling/ is a 404, and /en/stable/scalability/ mentions the
// backend only in its navigation. The same URL is already linked elsewhere in
// the app, so there is one answer to "where do we send an administrator".
const hpbDocsURL = "https://nextcloud-talk.readthedocs.io/en/stable/quick-install/"

// talkSignalingMode reads Talk's OWN signaling mode from Nextcloud's
// capabilities: "internal" when Talk signals by itself, "external" when a
// High Performance Backend is configured.
//
// This is deliberately not the connection probe's source. The probe learns the
// same fact from Talk's recording settings, which needs the recording
// credential AND a test room — so a deployment that fails either of those never
// finds out it has no HPB, which is the one fault that stops recording outright.
// Capabilities needs neither, and answers for the app's own identity.
func (c ExAppConfig) talkSignalingMode(ctx context.Context, client *http.Client) (string, error) {
	// As a USER, not as the app. Talk's capability provider needs a user
	// context to report its config, so the app identity gets a capabilities
	// document with no `spreed` block at all — which reads identically to "Talk
	// is not installed" and is why this check silently reported nothing on the
	// local harness.
	status, body, err := c.apiGetAs(ctx, client, c.provisioningUser(),
		strings.TrimRight(c.NextcloudURL, "/")+"/ocs/v2.php/cloud/capabilities")
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("capabilities -> %d", status)
	}
	var payload struct {
		OCS struct {
			Data struct {
				Capabilities struct {
					Spreed *struct {
						Config struct {
							Signaling struct {
								Mode string `json:"mode"`
							} `json:"signaling"`
						} `json:"config"`
					} `json:"spreed"`
				} `json:"capabilities"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode capabilities: %w", err)
	}
	spreed := payload.OCS.Data.Capabilities.Spreed
	if spreed == nil {
		// Talk is not installed. A different fault, and not this check's to
		// report as a signaling mode.
		return "", nil
	}
	return strings.TrimSpace(spreed.Config.Signaling.Mode), nil
}

// hpbFinding reports Talk's signaling backend, or nil when the probe's own
// talk.hpb finding should stand instead.
//
// needs_action, not warn: without a High Performance Backend Cassini cannot
// record at all, so this is not a degradation to note — it is the reason
// nothing works. It says what is lost and links to the backend's own docs
// rather than describing an installation procedure we cannot verify.
// hpbCheckForMode is the verdict for a signaling mode, split out so the mapping
// is testable without a Nextcloud to ask.
func hpbCheckForMode(mode string) *readinessCheck {
	switch mode {
	case "internal":
		return &readinessCheck{
			ID: "talk.hpb", State: "needs_action", Code: "hpb_disabled",
			// Scoped, not contrasted. "Cassini cannot record" followed by "calls
			// keep working" read as a contradiction; the second clause was meant
			// to bound the damage and instead undercut the first. So: what does
			// not work, why, and what the fault does NOT extend to.
			Message: "Recording cannot work until Talk has a High Performance Backend. Cassini records by joining the call as a hidden participant, and Talk only allows that through standalone signaling — here it is signalling by itself. Nothing else about Talk is affected.",
			Docs:    hpbDocsURL,
		}
	case "":
		// Talk absent, or too old to say. Not this check's business.
		return nil
	case "external":
		// A backend IS declared. This used to return nothing, on the grounds
		// that the connection probe owns the case — true when the probe gets
		// that far, and the probe routinely does not: it stops at
		// talk.discovery for a missing credential, a rejected one, or an
		// unreachable Nextcloud, and reports no talk.hpb row at all. The single
		// most important row then vanished from the checklist precisely when
		// something else was wrong. Saying what IS known beats saying nothing.
		return &readinessCheck{
			ID: "talk.hpb", State: "not_verified", Code: "hpb_declared_unverified",
			Message: "Talk names a High Performance Backend. Whether Cassini can authenticate to it is what the Talk connection check below establishes.",
			Action:  "recheck",
		}
	default:
		// A mode this build does not know. Nothing can be concluded from a word
		// we cannot interpret, and guessing either way would be worse than the
		// unchecked row the report falls back to.
		return nil
	}
}

func (rt *Runtime) hpbFinding(ctx context.Context) *readinessCheck {
	cfg, err := LoadExAppConfig()
	if err != nil || !cfg.Active {
		// Outside AppAPI there is no Nextcloud to ask. Not a fault.
		return nil
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	mode, err := cfg.talkSignalingMode(ctx, client)
	if err != nil {
		rt.logger.Printf("ERROR: could not read Talk's signaling mode: %v", err)
		return &readinessCheck{
			ID: "talk.hpb", State: "warn", Code: "signaling_mode_unknown",
			Message: "Cassini could not read Talk's signaling configuration, so it cannot tell whether a High Performance Backend is available.",
			Action:  "recheck",
		}
	}
	if mode == "" {
		// Said out loud rather than returning nothing. A missing `spreed` block
		// means Talk is absent OR that this request could not see Talk's config,
		// and the two are indistinguishable here — but a checklist that reports
		// neither is how this went unnoticed in the first place.
		rt.logger.Printf("WARNING: Nextcloud capabilities carried no Talk signaling mode; the recording backend cannot be reported")
	}
	return hpbCheckForMode(mode)
}

// backendUnknownCodes are the states in which no High Performance Backend is
// known to exist: it is absent, or nothing has established whether it is there.
var backendUnknownCodes = map[string]bool{
	"hpb_disabled":           true,
	"hpb_missing":            true,
	"hpb_not_checked":        true,
	"signaling_mode_unknown": true,
}

// mergeCredentialIntoBackend folds what is known about the signaling credential
// into the backend row.
//
// They were two rows reporting one thing. The credential is the High
// Performance Backend's own, it exists only to authenticate to it, and this row
// is where that attempt succeeds or fails — so a separate "Signaling server
// credential" row could only say whether a value had been SAVED. It said it as
// "Passed", on installations where nothing had ever tried to use the value, and
// where a backend existed without one both rows carried the same sentence and
// the same button. Its message even sent the reader to the Talk connection
// check, which authenticates with a different secret entirely and could not
// confirm this one.
//
// With no backend, the credential is not mentioned at all: there is nothing to
// authenticate to, and asking for it is how one missing backend turned into two
// rows wanting attention.
func mergeCredentialIntoBackend(row *readinessCheck, secret, source string) {
	if backendUnknownCodes[row.Code] {
		return
	}
	if strings.TrimSpace(secret) == "" {
		// Where to READ it, named. The same two locations the startup log has
		// always given; a row that asks for a secret without saying where it
		// lives sends an administrator hunting, which is what happened.
		row.State, row.Code = "needs_action", "internal_secret_missing"
		row.Message = "Talk has a High Performance Backend, and Cassini needs that server's internal secret to join calls invisibly. This is not a Nextcloud setting: it belongs to the signaling server, which is why Cassini cannot read it for you."
		row.Action = "configure_talk"
		row.Steps = []readinessStep{
			{Label: "Nextcloud All-in-One: docker exec nextcloud-aio-talk printenv INTERNAL_SECRET"},
			{Label: "Standalone signaling server: the `internalsecret` under `[clients]` in its configuration file"},
			{Label: "Paste it unchanged — one differing character fails exactly as a wrong credential would, and nothing can tell the difference until this check runs"},
		}
		return
	}
	// A secret IS saved, so the row keeps whatever the check found and simply
	// stays editable: without this the form is unreachable once the check
	// passes, and the value can never be rotated from here again.
	if row.Action == "" {
		row.Action = "configure_talk"
	}
	if row.Code == "hpb_declared_unverified" && source == "env" {
		row.Message = "Talk names a High Performance Backend, and the internal secret comes from Cassini's deployment configuration. Whether that secret is the right one is what this check establishes."
	} else if row.Code == "hpb_declared_unverified" {
		row.Message = "Talk names a High Performance Backend and an internal secret is saved. Whether Cassini can authenticate with it is what this check establishes."
	}
}
