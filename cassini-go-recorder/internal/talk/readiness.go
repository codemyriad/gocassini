package talk

import (
	"context"
	"errors"
	"strings"

	"gocassini/internal/config"
	"gocassini/internal/nextcloud"
	"gocassini/internal/signaling"
)

// ConnectionCheck contains only stable codes and safe messages. Never return
// upstream error bodies: they may contain credentials or private room details.
type ConnectionCheck struct {
	ID      string           `json:"id"`
	State   string           `json:"state"`
	Code    string           `json:"code"`
	Message string           `json:"message"`
	Action  string           `json:"action,omitempty"`
	Steps   []ConnectionStep `json:"steps,omitempty"`
}

type ConnectionStep struct {
	Label    string   `json:"label"`
	Commands []string `json:"commands,omitempty"`
}

// ProbeConnection authenticates exactly as the recorder does, but never joins
// a room, marks itself in-call, or subscribes to media.
func ProbeConnection(ctx context.Context, cfg config.Config) []ConnectionCheck {
	r := &Recorder{cfg: cfg}
	r.resolveProcessScopedTalkConfig()
	checks := []ConnectionCheck{}
	add := func(id, state, code, message, action string) {
		checks = append(checks, ConnectionCheck{ID: id, State: state, Code: code, Message: message, Action: action})
	}
	if err := r.resolveTalkTarget(); err != nil {
		// The room is Cassini's own, created by the operator, so a reader is
		// not the one who got it wrong — and "enter a valid URL" named a form
		// that no longer exists. Re-checking recreates the room.
		add("talk.discovery", "needs_action", "test_room_invalid", "Cassini's check conversation could not be resolved. Run this check again and Cassini will make a new one.", "recheck")
		return checks
	}
	if strings.TrimSpace(r.cfg.TalkRecordingSecret) == "" {
		add("talk.discovery", "needs_action", "recording_secret_missing", "Configure Cassini's recording credential and connect Talk first.", "connect_talk")
		return checks
	}
	base := strings.TrimRight(cfg.ConnectBaseURL, "/")
	if base == "" {
		base = r.baseURL
	}
	r.ocs = nextcloud.NewOCSClient(base, cfg.Insecure)
	settings, err := r.ocs.FetchRecordingSignalingSettings(ctx, r.roomToken, r.cfg.TalkRecordingSecret)
	if err != nil {
		var ocsErr *nextcloud.OCSError
		if errors.As(err, &ocsErr) && (ocsErr.HTTPStatus == 401 || ocsErr.HTTPStatus == 403) {
			add("talk.discovery", "needs_action", "recording_auth_rejected", "The Talk settings request was denied. Check the recording credential, access rules and recording-backend configuration.", "connect_talk")
		} else if errors.As(err, &ocsErr) && ocsErr.HTTPStatus == 404 {
			add("talk.discovery", "needs_action", "talk_or_room_unavailable", "Talk's recording settings are unavailable: either the Talk app is not enabled, or the conversation Cassini checks with is gone. Check the Talk app, then run this check again — Cassini will make a new conversation if it needs one.", "recheck")
		} else {
			// warn, not not_verified: the probe ran and could not reach
			// Nextcloud. That is a finding about the deployment, and reporting
			// it as an absence left a real fault rendering as a neutral "not
			// verified" row that flagged nothing (D-798).
			finding := classifyUnreachable(err)
			checks = append(checks, ConnectionCheck{ID: "talk.discovery", State: "warn", Code: finding.code, Message: finding.message, Action: "recheck", Steps: finding.steps})
		}
		return checks
	}
	add("talk.discovery", "passed", "recording_auth_verified", "Talk accepted Cassini's recording credential.", "")
	r.settings = settings
	if settings.PrimarySignalingServer() == "" {
		add("talk.hpb", "needs_action", "hpb_missing", "Talk has no standalone signaling server configured. Enable its high-performance backend.", "setup_hpb")
		return checks
	}
	if strings.TrimSpace(r.cfg.TalkSignalingInternalSecret) == "" {
		add("talk.hpb", "not_verified", "internal_secret_missing", "Talk has standalone signaling configured. Supply its internal client secret to verify HPB authentication.", "configure_talk")
		return checks
	}
	r.signaling = signaling.NewClient(toWSURL(settings.PrimarySignalingServer()), cfg.Insecure)
	if err := r.signaling.Connect(ctx); err != nil {
		// Reached for, and not reached. Same reasoning as nextcloud_unreachable.
		add("talk.hpb", "warn", "signaling_unreachable", "The configured signaling server could not be reached. Check its network route and TLS.", "recheck")
		return checks
	}
	defer r.signaling.Close()
	if err := r.hello(ctx); err != nil {
		state, code, message, action := "not_verified", "signaling_handshake_failed", "The signaling handshake did not finish. Check the server connection and try again.", "recheck"
		switch {
		case errors.Is(err, errInternalBackendRejected):
			// Nothing here is the reader's room to fix: the identity comes from
			// the Nextcloud URL Cassini was deployed with, so this is the
			// signaling server's backend list that has to allow it.
			state, code, message, action = "needs_action", "signaling_backend_rejected", "The signaling server does not recognise this Nextcloud as one of its backends. Check its backend configuration and the URL Cassini reaches Nextcloud on.", "setup_hpb"
		case errors.Is(err, errHPBUnsupported):
			state, code, message, action = "needs_action", "hpb_unsupported", "The signaling server does not advertise HPB media support.", "setup_hpb"
		case errors.Is(err, errInternalAuthFailed):
			state, code, message, action = "needs_action", "signaling_auth_failed", "HPB rejected internal-client authentication. Check the internal secret and server authentication configuration.", "configure_talk"
		case errors.Is(err, errInternalUnsupported):
			state, code, message, action = "needs_action", "internal_clients_disabled", "HPB does not accept internal clients. Ask its administrator to configure internalsecret.", "setup_hpb"
		}
		add("talk.hpb", state, code, message, action)
		return checks
	}

	add("talk.hpb", "passed", "hpb_authenticated", "The signaling server accepted Cassini and advertises media support. A test recording verifies the actual call path.", "")
	return checks
}

var errHPBUnsupported = errors.New("signaling server did not advertise MCU/HPB support")

var errInternalAuthFailed = errors.New("internal signaling auth failed")
var errInternalUnsupported = errors.New("internal clients are not supported by the signaling server; check that the signaling server internalsecret is configured")

var errInternalBackendRejected = errors.New("signaling server rejected the Nextcloud backend identity")
