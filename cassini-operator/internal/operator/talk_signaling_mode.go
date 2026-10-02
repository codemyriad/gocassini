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
