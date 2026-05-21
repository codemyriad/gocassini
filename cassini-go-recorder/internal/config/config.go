package config

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"
)

const (
	TalkAuthModeGuestParticipant = "guest-participant"
	TalkAuthModeHPBInternal      = "hpb-internal"
)

type Config struct {
	Mode                        string
	OutputPath                  string
	FinalOutputPath             string
	SegmentsDir                 string
	CleanupIntermediate         bool
	SimTracks                   int
	SimPackets                  int
	Duration                    time.Duration
	StopWhenRoomEmpty           bool
	RoomEmptyGrace              time.Duration
	CallURL                     string
	TalkBaseURL                 string
	TalkRoomToken               string
	TalkAuthMode                string
	ConnectBaseURL              string
	GuestName                   string
	JoinFlags                   int
	Insecure                    bool
	DebugSignaling              bool
	RequestOfferInterval        time.Duration
	MaxRequestOfferAttempts     int
	TurnMode                    string
	WriteReport                 bool
	TalkRecordingSecret         string
	TalkSignalingInternalSecret string
}

func NormalizeTalkAuthMode(value string) string {
	return strings.TrimSpace(value)
}

func ValidTalkAuthMode(value string) bool {
	switch NormalizeTalkAuthMode(value) {
	case TalkAuthModeGuestParticipant, TalkAuthModeHPBInternal:
		return true
	default:
		return false
	}
}

func FromFlags(args []string) (Config, error) {
	fs := flag.NewFlagSet("gocassini", flag.ContinueOnError)

	var cfg Config
	var durationSeconds int
	var roomEmptyGraceSeconds float64
	var requestOfferIntervalSeconds float64
	fs.StringVar(&cfg.Mode, "mode", "simulate", "simulate or talk")
	fs.StringVar(&cfg.OutputPath, "output", "/tmp/gocassini.csr", "output path; in talk mode this can be the final .mkv directly, while simulate mode still writes a legacy .csr archive")
	fs.StringVar(&cfg.FinalOutputPath, "final-output", "", "optional final multi-track MKV output path override (default: derived from --output)")
	fs.StringVar(&cfg.SegmentsDir, "segments-dir", "", "directory for intermediate per-session files (default: unique mktemp dir next to --final-output)")
	fs.BoolVar(&cfg.CleanupIntermediate, "cleanup-intermediate", false, "remove intermediate segment files after successful compose")
	fs.IntVar(&cfg.SimTracks, "sim-tracks", 3, "number of synthetic tracks in simulate mode")
	fs.IntVar(&cfg.SimPackets, "sim-packets", 150, "packets per synthetic track in simulate mode")
	fs.IntVar(&durationSeconds, "duration", 0, "hard runtime limit in seconds (0 disables fixed timeout)")
	fs.BoolVar(&cfg.StopWhenRoomEmpty, "stop-when-room-empty", true, "in talk mode, stop after all remote participants leave")
	fs.Float64Var(&roomEmptyGraceSeconds, "room-empty-grace", 30.0, "seconds to wait before stopping after room becomes empty")
	fs.StringVar(&cfg.CallURL, "call-url", "", "Nextcloud Talk call URL (required unless --talk-base-url and --talk-room-token are both provided in talk mode)")
	fs.StringVar(&cfg.TalkBaseURL, "talk-base-url", "", "Nextcloud base URL for the Talk room target")
	fs.StringVar(&cfg.TalkRoomToken, "talk-room-token", "", "Nextcloud Talk room token for the Talk room target")
	fs.StringVar(&cfg.TalkAuthMode, "talk-auth-mode", TalkAuthModeHPBInternal, "Talk bootstrap/auth mode: guest-participant or hpb-internal")
	fs.StringVar(&cfg.ConnectBaseURL, "connect-url", "", "optional Nextcloud base URL used for HTTP/OCS requests when different from the Talk room target")
	fs.StringVar(&cfg.GuestName, "name", "GocassiniObserver", "display name for the observer guest")
	fs.IntVar(&cfg.JoinFlags, "join-flags", 1, "call join flags (must include bit 1)")
	fs.BoolVar(&cfg.Insecure, "insecure", false, "disable TLS certificate verification (testing only)")
	fs.BoolVar(&cfg.DebugSignaling, "debug-signaling", false, "log signaling events and peer messages")
	fs.Float64Var(&requestOfferIntervalSeconds, "request-offer-interval", 2.0, "seconds between requestoffer retries (0 disables)")
	fs.IntVar(&cfg.MaxRequestOfferAttempts, "max-request-offer-attempts", 8, "max requestoffer attempts per session (0 disables limit)")
	fs.StringVar(&cfg.TurnMode, "turn-mode", "all", "TURN usage mode: off, udp-only, all")
	fs.BoolVar(&cfg.WriteReport, "write-report", false, "write legacy external JSON sidecar report next to the final MKV")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.Duration = time.Duration(durationSeconds) * time.Second
	cfg.RoomEmptyGrace = time.Duration(roomEmptyGraceSeconds * float64(time.Second))
	cfg.RequestOfferInterval = time.Duration(requestOfferIntervalSeconds * float64(time.Second))

	if cfg.Mode != "simulate" && cfg.Mode != "talk" {
		return Config{}, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	if cfg.Mode == "talk" && cfg.CallURL == "" && (strings.TrimSpace(cfg.TalkBaseURL) == "" || strings.TrimSpace(cfg.TalkRoomToken) == "") {
		return Config{}, errors.New("call-url or talk-base-url + talk-room-token must be provided in talk mode")
	}
	cfg.CallURL = strings.TrimSpace(cfg.CallURL)
	cfg.TalkBaseURL = strings.TrimRight(strings.TrimSpace(cfg.TalkBaseURL), "/")
	cfg.TalkRoomToken = strings.TrimSpace(cfg.TalkRoomToken)
	cfg.TalkAuthMode = NormalizeTalkAuthMode(cfg.TalkAuthMode)
	if cfg.ConnectBaseURL != "" {
		cfg.ConnectBaseURL = strings.TrimRight(strings.TrimSpace(cfg.ConnectBaseURL), "/")
	}
	if durationSeconds < 0 {
		return Config{}, errors.New("duration must be >= 0")
	}
	if cfg.OutputPath == "" {
		return Config{}, errors.New("output path must not be empty")
	}
	if (cfg.TalkBaseURL == "") != (cfg.TalkRoomToken == "") {
		return Config{}, errors.New("talk-base-url and talk-room-token must be provided together")
	}
	if cfg.TalkAuthMode != "" && !ValidTalkAuthMode(cfg.TalkAuthMode) {
		return Config{}, fmt.Errorf("invalid talk-auth-mode %q", cfg.TalkAuthMode)
	}
	if cfg.SimTracks < 1 {
		return Config{}, errors.New("sim-tracks must be >= 1")
	}
	if cfg.SimPackets < 1 {
		return Config{}, errors.New("sim-packets must be >= 1")
	}
	if cfg.JoinFlags <= 0 {
		return Config{}, errors.New("join-flags must be > 0")
	}
	if (cfg.JoinFlags & 1) == 0 {
		return Config{}, errors.New("join-flags must include bit 1 (in-call)")
	}
	if cfg.RequestOfferInterval < 0 {
		return Config{}, errors.New("request-offer-interval must be >= 0")
	}
	if cfg.RoomEmptyGrace < 0 {
		return Config{}, errors.New("room-empty-grace must be >= 0")
	}
	if cfg.MaxRequestOfferAttempts < 0 {
		return Config{}, errors.New("max-request-offer-attempts must be >= 0")
	}
	switch cfg.TurnMode {
	case "off", "udp-only", "all":
	default:
		return Config{}, fmt.Errorf("invalid turn-mode %q", cfg.TurnMode)
	}

	return cfg, nil
}
