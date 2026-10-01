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
	status, body, err := c.apiGet(ctx, client, strings.TrimRight(c.NextcloudURL, "/")+"/ocs/v2.php/cloud/capabilities")
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
			Message: "Talk is signalling by itself, with no High Performance Backend. Cassini cannot record without one: recording joins a call as a participant, which Talk only supports through standalone signaling. Calls between people keep working.",
			Docs:    hpbDocsURL,
		}
	case "":
		// Talk absent, or too old to say. Not this check's business.
		return nil
	default:
		// external, or a mode this build does not know. A backend exists; the
		// connection probe verifies Cassini can authenticate to it.
		return nil
	}
}

func (rt *Runtime) hpbFinding(ctx context.Context) *readinessCheck {
	cfg, err := LoadExAppConfig()
	if err != nil || !cfg.Active {
		return nil
	}
	client := &http.Client{Timeout: ncProvisionTimeout}
	mode, err := cfg.talkSignalingMode(ctx, client)
	if err != nil {
		return &readinessCheck{
			ID: "talk.hpb", State: "warn", Code: "signaling_mode_unknown",
			Message: "Cassini could not read Talk's signaling configuration, so it cannot tell whether a High Performance Backend is available.",
			Action:  "recheck",
		}
	}
	return hpbCheckForMode(mode)
}
