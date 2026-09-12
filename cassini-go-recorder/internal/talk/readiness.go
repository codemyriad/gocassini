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
	ID      string `json:"id"`
	State   string `json:"state"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Action  string `json:"action,omitempty"`
}

// ProbeConnection authenticates exactly as the recorder does, but never joins
// a room, marks itself in-call, or subscribes to media.
func ProbeConnection(ctx context.Context, cfg config.Config) []ConnectionCheck {
	r := &Recorder{cfg: cfg}
	r.resolveProcessScopedTalkConfig()
	checks := []ConnectionCheck{}
	add := func(id, state, code, message, action string) {
		checks = append(checks, ConnectionCheck{id, state, code, message, action})
	}
	if err := r.resolveTalkTarget(); err != nil {
		add("talk.discovery", "needs_action", "test_room_invalid", "Enter a valid Talk test-room URL.", "test_room")
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
			add("talk.discovery", "needs_action", "talk_or_room_unavailable", "Talk's recording settings are unavailable. Check that Talk is enabled and the test room exists.", "test_room")
		} else {
			add("talk.discovery", "not_verified", "nextcloud_unreachable", "Could not read Talk settings. Check Nextcloud connectivity and TLS, then try again.", "recheck")
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
		add("talk.hpb", "not_verified", "signaling_unreachable", "The configured signaling server could not be reached. Check its network route and TLS.", "recheck")
		return checks
	}
	defer r.signaling.Close()
	if err := r.hello(ctx); err != nil {
		state, code, message, action := "not_verified", "signaling_handshake_failed", "The signaling handshake did not finish. Check the server connection and try again.", "recheck"
		switch {
		case errors.Is(err, errInternalBackendRejected):
			state, code, message, action = "needs_action", "signaling_backend_rejected", "HPB did not recognize the Nextcloud backend identity supplied by the test-room URL. Check the public room URL and HPB backend configuration.", "test_room"
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

	add("talk.hpb", "passed", "hpb_authenticated", "HPB authenticated Cassini using the test-room URL's backend identity and advertised media support. A Talk recording verifies the actual call path.", "")
	return checks
}

var errHPBUnsupported = errors.New("signaling server did not advertise MCU/HPB support")

var errInternalAuthFailed = errors.New("internal signaling auth failed")
var errInternalUnsupported = errors.New("internal clients are not supported by the signaling server; check that the signaling server internalsecret is configured")

var errInternalBackendRejected = errors.New("signaling server rejected the Nextcloud backend identity")
