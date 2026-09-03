package cassini

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
)

const (
	devStackPublicLocalHTTP  = "local-http"
	devStackPublicLANHTTP    = "lan-http"
	devStackPublicRemoteHTTP = "remote-https"

	devStackServiceLegacyDefault = "legacy-default"
	devStackServiceCore          = "core"
	devStackServiceAppAPI        = "appapi"
	devStackServiceFull          = "full"
	devStackServiceFullRemote    = "full-remote"

	devStackCassiniNone           = "none"
	devStackCassiniInstalledExApp = "installed-exapp"

	devStackRecordingLegacy         = "legacy"
	devStackRecordingDirectOperator = "direct-operator"
	devStackRecordingInstalledExApp = "installed-exapp"
	devStackRecordingNone           = "none"

	devStackImageBuild      = "build"
	devStackImageReuseLocal = "reuse-local"
	devStackImagePull       = "pull"

	devStackPatchAuto  = "auto"
	devStackPatchNone  = "none"
	devStackPatchForce = "force"

	devStackExistingFail   = "fail"
	devStackExistingResume = "resume"
	devStackExistingReset  = "reset"

	// Which storage model the stack is brought up in, and which the ExApp is
	// told to start in. Explicit rather than implied: the harness used to build
	// the access-controlled substrate unconditionally, so "default mode" and
	// "the substrate has not finished appearing yet" looked identical — and the
	// ExApp derived its mode from whichever it happened to see.
	devStackStorageDefault = "default"
	devStackStorageACL     = "acl-enabled"
)

type devStackPlan struct {
	Command               string
	PublicMode            string
	PublicURL             string
	PublicHost            string
	MediaHost             string
	SignalingPublicURL    string
	TalkBackendURL        string
	ServiceMode           string
	SpreedProfile         string
	CassiniMode           string
	RecordingBackend      string
	ExAppImageMode        string
	PatchMode             string
	ExistingResourceMode  string
	StorageMode           string
	SkipStorageScaffold   bool
	DownSuspend           bool
	DownVolumes           bool
	DownFull              bool
	BuildRequested        bool
	RemoteConfigRequested bool
	ValidationWarnings    []string
}

type devStackFlagOptions struct {
	publicMode          string
	publicURL           string
	publicHost          string
	mediaHost           string
	signalingPublicURL  string
	talkBackendURL      string
	serviceMode         string
	cassiniMode         string
	recordingBackend    string
	exAppImageMode      string
	patchMode           string
	storageMode         string
	skipStorageScaffold bool
	build               bool
	resume              bool
	reset               bool
	suspend             bool
	downVolumes         bool
	downFull            bool
	set                 map[string]bool
}

type envLookupFunc func(string) (string, bool)

func osEnvLookup(key string) (string, bool) { return os.LookupEnv(key) }

func parseDevStackFlags(command string, args []string) (devStackFlagOptions, []string, error) {
	opts := devStackFlagOptions{set: map[string]bool{}}
	fs := flag.NewFlagSet("cassini dev stack "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	stringFlag := func(name, usage string) *string {
		return fs.String(name, "", usage)
	}

	publicMode := stringFlag("public-mode", "public/browser mode: local-http, lan-http, remote-https")
	publicURL := stringFlag("public-url", "browser-facing public Nextcloud URL")
	publicHost := stringFlag("public-host", "browser-facing public host without scheme")
	mediaHost := stringFlag("media-host", "host/IP advertised for WebRTC media")
	signalingPublicURL := stringFlag("signaling-public-url", "browser-facing standalone signaling URL")
	talkBackendURL := stringFlag("talk-backend-url", "Nextcloud URL used by the Cassini ExApp for Talk callbacks")
	services := stringFlag("services", "service topology: legacy-default, core, appapi, full, full-remote")
	serviceMode := stringFlag("service-mode", "alias for --services")
	cassiniMode := stringFlag("cassini", "Cassini install mode: none, installed-exapp")
	recordingBackend := stringFlag("recording-backend", "Talk recording backend: legacy, direct-operator, installed-exapp, none")
	exAppImageMode := stringFlag("exapp-image-mode", "ExApp image mode: build, reuse-local, pull")
	patchMode := stringFlag("patch", "patch mode: auto, none, force")
	storageMode := stringFlag("storage-mode", "recording storage mode the stack is built in and the ExApp starts in: default, acl-enabled")
	skipStorageScaffold := fs.Bool("debug-skip-storage-scaffold", false,
		"debug: build no recordings storage at all — no cassini service account, no Team folder, and neither native app")
	build := fs.Bool("build", false, "build the Cassini ExApp image before registration")
	resume := fs.Bool("resume", false, "reuse matching stopped containers or retained harness volumes")
	reset := fs.Bool("reset", false, "stop/remove/recreate resources for the resolved stack")
	suspend := fs.Bool("suspend", false, "for stack down: stop containers but keep them for 'up --resume'")
	downVolumes := fs.Bool("volumes", false, "for stack down: remove containers and volumes for the current config")
	downFull := fs.Bool("full", false, "for stack down: remove all harness-owned resources, including volumes and ExApp")

	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}

	fs.Visit(func(f *flag.Flag) { opts.set[f.Name] = true })
	if *serviceMode != "" && *services != "" && *serviceMode != *services {
		return opts, nil, fmt.Errorf("--services and --service-mode disagree: %q vs %q", *services, *serviceMode)
	}
	if *services == "" {
		*services = *serviceMode
	}
	if opts.set["service-mode"] {
		opts.set["services"] = true
	}

	opts.publicMode = *publicMode
	opts.publicURL = *publicURL
	opts.publicHost = *publicHost
	opts.mediaHost = *mediaHost
	opts.signalingPublicURL = *signalingPublicURL
	opts.talkBackendURL = *talkBackendURL
	opts.serviceMode = *services
	opts.cassiniMode = *cassiniMode
	opts.recordingBackend = *recordingBackend
	opts.exAppImageMode = *exAppImageMode
	opts.patchMode = *patchMode
	opts.storageMode = *storageMode
	opts.skipStorageScaffold = *skipStorageScaffold
	opts.build = *build
	opts.resume = *resume
	opts.reset = *reset
	opts.suspend = *suspend
	opts.downVolumes = *downVolumes
	opts.downFull = *downFull
	return opts, fs.Args(), nil
}

func resolveDevStackPlan(command string, args []string, lookup envLookupFunc) (devStackPlan, []string, error) {
	opts, rest, err := parseDevStackFlags(command, args)
	if err != nil {
		return devStackPlan{}, nil, err
	}

	plan := devStackPlan{Command: command}

	get := func(key string) string {
		value, _ := lookup(key)
		return value
	}
	// Config hierarchy: explicit flag > env var > default. Empty env values
	// count as unset (matching the shell-side guard in harness common.sh).
	pick := func(flagName, flagValue, envName, fallback string) string {
		if opts.set[flagName] {
			return flagValue
		}
		if value := get(envName); value != "" {
			return value
		}
		return fallback
	}

	plan.PublicMode = pick("public-mode", opts.publicMode, "CASSINI_HARNESS_PUBLIC_MODE", devStackPublicLocalHTTP)

	// An explicitly flagged non-remote public mode overrides ambient remote
	// env vars entirely: env-sourced remote inputs are ignored rather than
	// failing the plan. Remote inputs passed as flags stay contradictory.
	maskRemoteEnv := opts.set["public-mode"] && plan.PublicMode != devStackPublicRemoteHTTP
	pickRemoteInput := func(flagName, flagValue, envName string) string {
		if opts.set[flagName] {
			return flagValue
		}
		if maskRemoteEnv {
			return ""
		}
		return get(envName)
	}

	plan.PublicURL = pickRemoteInput("public-url", opts.publicURL, "CASSINI_HARNESS_PUBLIC_URL")
	plan.PublicHost = pickRemoteInput("public-host", opts.publicHost, "CASSINI_HARNESS_PUBLIC_HOST")
	plan.MediaHost = pickRemoteInput("media-host", opts.mediaHost, "CASSINI_HARNESS_MEDIA_HOST")
	plan.SignalingPublicURL = pickRemoteInput("signaling-public-url", opts.signalingPublicURL, "CASSINI_HARNESS_SIGNALING_PUBLIC_URL")
	plan.TalkBackendURL = pick("talk-backend-url", opts.talkBackendURL, "CASSINI_TALK_BACKEND_URL", "")
	plan.ServiceMode = pick("services", opts.serviceMode, "CASSINI_HARNESS_SERVICE_MODE", "")
	plan.CassiniMode = pick("cassini", opts.cassiniMode, "CASSINI_HARNESS_CASSINI_MODE", devStackCassiniNone)
	plan.RecordingBackend = pick("recording-backend", opts.recordingBackend, "CASSINI_HARNESS_RECORDING_BACKEND", devStackRecordingLegacy)
	plan.ExAppImageMode = pick("exapp-image-mode", opts.exAppImageMode, "CASSINI_HARNESS_EXAPP_IMAGE_MODE", devStackImageReuseLocal)
	plan.PatchMode = pick("patch", opts.patchMode, "CASSINI_HARNESS_PATCH_MODE", devStackPatchAuto)
	// Access-controlled is the harness default because it is what the harness
	// has always built, and what the e2e suites assert. Production defaults the
	// other way, by deriving: a fresh install has no Team folder, so it lands on
	// the deps-free model without anyone declaring anything.
	plan.StorageMode = pick("storage-mode", opts.storageMode, "CASSINI_HARNESS_STORAGE_MODE", devStackStorageDefault)
	plan.SkipStorageScaffold = opts.skipStorageScaffold ||
		(!opts.set["debug-skip-storage-scaffold"] && get("CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD") == "1")
	plan.DownSuspend = opts.suspend
	plan.DownVolumes = opts.downVolumes
	plan.DownFull = opts.downFull
	plan.BuildRequested = opts.build

	if opts.build {
		plan.ExAppImageMode = devStackImageBuild
	}
	if command != "up" && (opts.resume || opts.reset) {
		return plan, rest, errors.New("--resume and --reset apply only to stack up")
	}
	if command != "down" && (opts.suspend || opts.downVolumes || opts.downFull) {
		return plan, rest, errors.New("--suspend, --volumes, and --full apply only to stack down")
	}
	if opts.suspend && (opts.downVolumes || opts.downFull) {
		return plan, rest, errors.New("--suspend keeps containers and cannot be combined with --volumes or --full")
	}

	switch {
	case opts.resume && opts.reset:
		return plan, rest, errors.New("--resume and --reset are mutually exclusive")
	case opts.resume:
		plan.ExistingResourceMode = devStackExistingResume
	case opts.reset:
		plan.ExistingResourceMode = devStackExistingReset
	default:
		if value := get("CASSINI_HARNESS_EXISTING"); value != "" {
			plan.ExistingResourceMode = value
		} else {
			plan.ExistingResourceMode = devStackExistingFail
		}
	}

	if plan.ServiceMode == "" {
		plan.ServiceMode = devStackServiceLegacyDefault
	}
	plan.ServiceMode = normalizeDevStackServiceMode(plan.ServiceMode)
	plan.SpreedProfile = resolveDevStackSpreedProfile(plan.ServiceMode, get("SPREED_PROFILE"))

	if plan.PublicURL != "" {
		plan.PublicURL = strings.TrimRight(plan.PublicURL, "/")
	}
	if plan.PublicHost == "" && plan.PublicURL != "" {
		if host, ok := devStackURLHost(plan.PublicURL); ok {
			plan.PublicHost = host
		}
	}
	if plan.PublicURL == "" && plan.PublicMode == devStackPublicRemoteHTTP && plan.PublicHost != "" {
		plan.PublicURL = "https://" + plan.PublicHost
	}
	if plan.SignalingPublicURL == "" && plan.PublicMode == devStackPublicRemoteHTTP && plan.PublicHost != "" {
		plan.SignalingPublicURL = "https://" + plan.PublicHost + ":8443"
	}
	plan.RemoteConfigRequested = plan.PublicMode == devStackPublicRemoteHTTP || plan.ServiceMode == devStackServiceFullRemote

	if plan.PublicMode == devStackPublicLocalHTTP {
		remoteInputs := []string{}
		for _, input := range []struct {
			flag string
			env  string
		}{
			{"public-url", "CASSINI_HARNESS_PUBLIC_URL"},
			{"public-host", "CASSINI_HARNESS_PUBLIC_HOST"},
			{"media-host", "CASSINI_HARNESS_MEDIA_HOST"},
			{"signaling-public-url", "CASSINI_HARNESS_SIGNALING_PUBLIC_URL"},
		} {
			if opts.set[input.flag] || (!maskRemoteEnv && get(input.env) != "") {
				remoteInputs = append(remoteInputs, "--"+input.flag+"/"+input.env)
			}
		}
		if len(remoteInputs) > 0 {
			sort.Strings(remoteInputs)
			return plan, rest, fmt.Errorf("remote harness inputs require --public-mode %s: %s", devStackPublicRemoteHTTP, strings.Join(remoteInputs, ", "))
		}
	}

	if err := validateDevStackPlan(plan); err != nil {
		return plan, rest, err
	}
	plan.ValidationWarnings = collectDevStackWarnings(plan, opts, lookup)
	return plan, rest, nil
}

func collectDevStackWarnings(plan devStackPlan, opts devStackFlagOptions, lookup envLookupFunc) []string {
	var warnings []string

	envValue := func(name string) (string, bool) {
		value, ok := lookup(name)
		return value, ok && value != ""
	}
	inputSet := func(flagName, envName string) bool {
		if opts.set[flagName] {
			return true
		}
		_, ok := envValue(envName)
		return ok
	}

	if spreadEnv, ok := envValue("SPREED_PROFILE"); ok && spreadEnv != plan.SpreedProfile && oneOf(plan.ServiceMode, devStackServiceCore, devStackServiceAppAPI, devStackServiceFull, devStackServiceFullRemote) {
		warnings = append(warnings, fmt.Sprintf("SPREED_PROFILE=%s is ignored because service mode %s forces SPREED_PROFILE=%s.", spreadEnv, plan.ServiceMode, plan.SpreedProfile))
	}
	if plan.CassiniMode == devStackCassiniNone && inputSet("exapp-image-mode", "CASSINI_HARNESS_EXAPP_IMAGE_MODE") {
		warnings = append(warnings, fmt.Sprintf("ExApp image mode %s is ignored because Cassini mode is none.", plan.ExAppImageMode))
	}
	if plan.CassiniMode == devStackCassiniNone && oneOf(plan.PatchMode, devStackPatchNone, devStackPatchForce) && inputSet("patch", "CASSINI_HARNESS_PATCH_MODE") {
		warnings = append(warnings, fmt.Sprintf("Patch mode %s is ignored because Cassini mode is none; no AppAPI CSP patch will run.", plan.PatchMode))
	}

	hasMediaStack := devStackHasMediaStack(plan)
	if !hasMediaStack && (plan.MediaHost != "" || plan.SignalingPublicURL != "") {
		warnings = append(warnings, "Media/signaling remote inputs are ignored because the resolved service topology does not start the Talk media stack.")
	}
	if plan.CassiniMode == devStackCassiniInstalledExApp && oneOf(plan.RecordingBackend, devStackRecordingLegacy, devStackRecordingDirectOperator) {
		warnings = append(warnings, fmt.Sprintf("Cassini is installed as an ExApp, but Talk recording uses the %s backend; the installed ExApp will not receive recording callbacks.", plan.RecordingBackend))
	}
	if !hasMediaStack && plan.RecordingBackend != devStackRecordingNone && inputSet("recording-backend", "CASSINI_HARNESS_RECORDING_BACKEND") {
		warnings = append(warnings, fmt.Sprintf("Recording backend %s will not be configured because the resolved service topology does not start the Talk media stack; use recording backend none for install-only checks.", plan.RecordingBackend))
	}
	if hasMediaStack && plan.PublicMode == devStackPublicRemoteHTTP && isRFC1918IPv4Host(plan.MediaHost) {
		warnings = append(warnings, fmt.Sprintf("remote-https media host %s is private/RFC1918; browsers outside that private network will not reach WebRTC media.", plan.MediaHost))
	}

	if plan.ExistingResourceMode == devStackExistingReset && oneOf(plan.Command, "plan", "up") {
		if plan.CassiniMode == devStackCassiniInstalledExApp {
			warnings = append(warnings, "Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes and installed ExApp state.")
		} else {
			warnings = append(warnings, "Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes.")
		}
	}
	if plan.Command == "down" && plan.DownVolumes {
		warnings = append(warnings, "down --volumes will remove current-project containers and Docker Compose volumes, plus installed ExApp containers and state volumes if present.")
	}
	if plan.Command == "down" && plan.DownFull {
		warnings = append(warnings, "down --full will remove all known harness-owned Compose and installed ExApp resources, including volumes.")
	}

	return warnings
}

func devStackHasMediaStack(plan devStackPlan) bool {
	return oneOf(plan.ServiceMode, devStackServiceFull, devStackServiceFullRemote) || plan.SpreedProfile == "full"
}

func isRFC1918IPv4Host(host string) bool {
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !addr.Is4() {
		return false
	}
	octets := addr.As4()
	return octets[0] == 10 ||
		(octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31) ||
		(octets[0] == 192 && octets[1] == 168)
}

func normalizeDevStackServiceMode(value string) string {
	switch value {
	case "legacy", "default":
		return devStackServiceLegacyDefault
	default:
		return value
	}
}

func resolveDevStackSpreedProfile(serviceMode, spreadEnv string) string {
	switch serviceMode {
	case devStackServiceFull, devStackServiceFullRemote:
		return "full"
	case devStackServiceCore, devStackServiceAppAPI:
		return "default"
	case devStackServiceLegacyDefault:
		if spreadEnv != "" {
			return spreadEnv
		}
		return "full"
	default:
		if spreadEnv != "" {
			return spreadEnv
		}
		return "full"
	}
}

func validateDevStackPlan(plan devStackPlan) error {
	if !oneOf(plan.PublicMode, devStackPublicLocalHTTP, devStackPublicLANHTTP, devStackPublicRemoteHTTP) {
		return fmt.Errorf("invalid public mode %q", plan.PublicMode)
	}
	if !oneOf(plan.ServiceMode, devStackServiceLegacyDefault, devStackServiceCore, devStackServiceAppAPI, devStackServiceFull, devStackServiceFullRemote) {
		return fmt.Errorf("invalid service mode %q", plan.ServiceMode)
	}
	if !oneOf(plan.CassiniMode, devStackCassiniNone, devStackCassiniInstalledExApp) {
		return fmt.Errorf("invalid Cassini mode %q", plan.CassiniMode)
	}
	if !oneOf(plan.RecordingBackend, devStackRecordingLegacy, devStackRecordingDirectOperator, devStackRecordingInstalledExApp, devStackRecordingNone) {
		return fmt.Errorf("invalid recording backend %q", plan.RecordingBackend)
	}
	if !oneOf(plan.ExAppImageMode, devStackImageBuild, devStackImageReuseLocal, devStackImagePull) {
		return fmt.Errorf("invalid ExApp image mode %q", plan.ExAppImageMode)
	}
	if !oneOf(plan.PatchMode, devStackPatchAuto, devStackPatchNone, devStackPatchForce) {
		return fmt.Errorf("invalid patch mode %q", plan.PatchMode)
	}
	if !oneOf(plan.ExistingResourceMode, devStackExistingFail, devStackExistingResume, devStackExistingReset) {
		return fmt.Errorf("invalid existing-resource mode %q", plan.ExistingResourceMode)
	}
	if !oneOf(plan.StorageMode, devStackStorageDefault, devStackStorageACL) {
		return fmt.Errorf("invalid storage mode %q (want %s or %s)", plan.StorageMode, devStackStorageDefault, devStackStorageACL)
	}
	if plan.PublicHost != "" && strings.Contains(plan.PublicHost, "://") {
		return fmt.Errorf("public host must be a bare host, got %q", plan.PublicHost)
	}
	if plan.PublicHost != "" && strings.Contains(plan.PublicHost, "/") {
		return fmt.Errorf("public host must not contain a path, got %q", plan.PublicHost)
	}
	if plan.PublicMode == devStackPublicRemoteHTTP {
		if plan.PublicURL == "" || plan.PublicHost == "" {
			return errors.New("remote-https mode requires --public-url or --public-host")
		}
		if scheme, ok := devStackURLScheme(plan.PublicURL); !ok || scheme != "https" {
			return fmt.Errorf("remote-https mode requires an https public URL, got %q", plan.PublicURL)
		}
		if plan.MediaHost == "" {
			return errors.New("remote-https mode requires --media-host / CASSINI_HARNESS_MEDIA_HOST")
		}
		if isLoopbackHost(plan.MediaHost) {
			return fmt.Errorf("remote-https media host must be non-loopback, got %q", plan.MediaHost)
		}
		if plan.SignalingPublicURL != "" {
			if scheme, ok := devStackURLScheme(plan.SignalingPublicURL); !ok || scheme != "https" {
				return fmt.Errorf("remote-https signaling public URL must be https, got %q", plan.SignalingPublicURL)
			}
		}
	}
	if plan.PublicMode == devStackPublicLANHTTP {
		if plan.PublicURL == "" {
			return errors.New("lan-http mode requires --public-url / CASSINI_HARNESS_PUBLIC_URL")
		}
		if scheme, ok := devStackURLScheme(plan.PublicURL); !ok || scheme != "http" {
			return fmt.Errorf("lan-http public URL must be http, got %q", plan.PublicURL)
		}
		if devStackHasMediaStack(plan) {
			if plan.MediaHost == "" {
				return errors.New("lan-http media mode requires --media-host / CASSINI_HARNESS_MEDIA_HOST")
			}
			if isLoopbackHost(plan.MediaHost) {
				return fmt.Errorf("lan-http media host must be non-loopback, got %q", plan.MediaHost)
			}
			if plan.SignalingPublicURL == "" {
				return errors.New("lan-http media mode requires --signaling-public-url / CASSINI_HARNESS_SIGNALING_PUBLIC_URL")
			}
			if scheme, ok := devStackURLScheme(plan.SignalingPublicURL); !ok || scheme != "http" {
				return fmt.Errorf("lan-http signaling public URL must be http, got %q", plan.SignalingPublicURL)
			}
			signalingHost, _ := devStackURLHost(plan.SignalingPublicURL)
			if isLoopbackHost(signalingHost) {
				return fmt.Errorf("lan-http signaling public URL must use a non-loopback host, got %q", plan.SignalingPublicURL)
			}
		}
	}
	if plan.TalkBackendURL != "" {
		if scheme, ok := devStackURLScheme(plan.TalkBackendURL); !ok || !oneOf(scheme, "http", "https") {
			return fmt.Errorf("Talk backend URL must be http or https, got %q", plan.TalkBackendURL)
		}
	}
	if plan.PublicMode == devStackPublicLANHTTP && plan.RecordingBackend == devStackRecordingInstalledExApp && plan.TalkBackendURL == "" {
		return errors.New("lan-http installed-ExApp recording requires --talk-backend-url / CASSINI_TALK_BACKEND_URL")
	}
	if plan.ServiceMode == devStackServiceFullRemote && plan.PublicMode != devStackPublicRemoteHTTP {
		return fmt.Errorf("service mode %q requires --public-mode %s", devStackServiceFullRemote, devStackPublicRemoteHTTP)
	}
	if plan.RecordingBackend == devStackRecordingInstalledExApp && plan.CassiniMode != devStackCassiniInstalledExApp {
		return errors.New("recording backend installed-exapp requires --cassini installed-exapp")
	}
	if (plan.RecordingBackend == devStackRecordingDirectOperator || plan.RecordingBackend == devStackRecordingInstalledExApp) && (plan.ServiceMode == devStackServiceCore || plan.ServiceMode == devStackServiceAppAPI) {
		return fmt.Errorf("recording backend %s requires service mode full, full-remote, or legacy-default", plan.RecordingBackend)
	}
	if plan.CassiniMode == devStackCassiniInstalledExApp && plan.ServiceMode == devStackServiceCore {
		return errors.New("--cassini installed-exapp requires service mode appapi, full, full-remote, or legacy-default")
	}
	if plan.CassiniMode == devStackCassiniNone && plan.ExAppImageMode == devStackImageBuild {
		return errors.New("--build / ExApp image mode build requires --cassini installed-exapp")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func devStackURLHost(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Hostname(), true
}

func devStackURLScheme(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	return strings.ToLower(u.Scheme), true
}

func isLoopbackHost(host string) bool {
	h := strings.Trim(strings.ToLower(host), "[]")
	if h == "localhost" || h == "::1" || h == "127.0.0.1" {
		return true
	}
	if strings.HasPrefix(h, "127.") {
		return true
	}
	return false
}

// devStackExAppStorageMode translates the harness's word into the app's. They
// differ on purpose: `acl-enabled` is what reads well on a command line, and
// `access_controlled` is the one vocabulary the config file, the API and the UI
// already share.
func devStackExAppStorageMode(storageMode string) string {
	if storageMode == devStackStorageACL {
		return "access_controlled"
	}
	return "default"
}

func boolEnv(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (plan devStackPlan) env() []string {
	// Remote inputs are always emitted, even when empty: the resolved plan is
	// the single source of truth for child scripts, and an empty assignment
	// masks ambient shell values (harness common.sh treats empty as unset).
	return []string{
		"CASSINI_HARNESS_PUBLIC_MODE=" + plan.PublicMode,
		"CASSINI_HARNESS_SERVICE_MODE=" + plan.ServiceMode,
		"CASSINI_HARNESS_CASSINI_MODE=" + plan.CassiniMode,
		"CASSINI_HARNESS_RECORDING_BACKEND=" + plan.RecordingBackend,
		"CASSINI_HARNESS_EXAPP_IMAGE_MODE=" + plan.ExAppImageMode,
		"CASSINI_HARNESS_PATCH_MODE=" + plan.PatchMode,
		"CASSINI_HARNESS_EXISTING=" + plan.ExistingResourceMode,
		"CASSINI_HARNESS_STORAGE_MODE=" + plan.StorageMode,
		"CASSINI_HARNESS_SKIP_STORAGE_SCAFFOLD=" + boolEnv(plan.SkipStorageScaffold),
		// What the ExApp itself is told. The harness speaks `acl-enabled`; the
		// app's own vocabulary — its config file, its API, its UI — says
		// `access_controlled`, and that is what crosses the boundary.
		"CASSINI_STORAGE_MODE=" + devStackExAppStorageMode(plan.StorageMode),
		"SPREED_PROFILE=" + plan.SpreedProfile,
		"CASSINI_HARNESS_PUBLIC_URL=" + plan.PublicURL,
		"CASSINI_HARNESS_PUBLIC_HOST=" + plan.PublicHost,
		"CASSINI_HARNESS_MEDIA_HOST=" + plan.MediaHost,
		"CASSINI_HARNESS_SIGNALING_PUBLIC_URL=" + plan.SignalingPublicURL,
		"CASSINI_TALK_BACKEND_URL=" + plan.TalkBackendURL,
	}
}

func printDevStackPlan(w io.Writer, plan devStackPlan) {
	fmt.Fprintf(w, "command: %s\n", plan.Command)
	fmt.Fprintln(w, "public:")
	fmt.Fprintf(w, "  mode: %s\n", plan.PublicMode)
	fmt.Fprintf(w, "  url: %s\n", yamlValueOrNull(plan.PublicURL))
	fmt.Fprintf(w, "  host: %s\n", yamlValueOrNull(plan.PublicHost))
	fmt.Fprintf(w, "  media_host: %s\n", yamlValueOrNull(plan.MediaHost))
	fmt.Fprintf(w, "  signaling_url: %s\n", yamlValueOrNull(plan.SignalingPublicURL))
	fmt.Fprintf(w, "  remote_config_requested: %t\n", plan.RemoteConfigRequested)
	fmt.Fprintln(w, "services:")
	fmt.Fprintf(w, "  mode: %s\n", plan.ServiceMode)
	fmt.Fprintf(w, "  spreed_profile: %s\n", plan.SpreedProfile)
	fmt.Fprintln(w, "cassini:")
	fmt.Fprintf(w, "  mode: %s\n", plan.CassiniMode)
	fmt.Fprintf(w, "  exapp_image_mode: %s\n", plan.ExAppImageMode)
	fmt.Fprintln(w, "recording:")
	fmt.Fprintf(w, "  backend: %s\n", plan.RecordingBackend)
	fmt.Fprintf(w, "  talk_backend_url: %s\n", yamlValueOrNull(plan.TalkBackendURL))
	fmt.Fprintln(w, "patch:")
	fmt.Fprintf(w, "  mode: %s\n", plan.PatchMode)
	fmt.Fprintln(w, "storage:")
	fmt.Fprintf(w, "  mode: %s\n", plan.StorageMode)
	fmt.Fprintf(w, "  exapp_initial_mode: %s\n", devStackExAppStorageMode(plan.StorageMode))
	fmt.Fprintf(w, "  skip_scaffold: %t\n", plan.SkipStorageScaffold)
	fmt.Fprintln(w, "lifecycle:")
	fmt.Fprintf(w, "  existing_resources: %s\n", plan.ExistingResourceMode)
	fmt.Fprintf(w, "  down_suspend: %t\n", plan.DownSuspend)
	fmt.Fprintf(w, "  down_volumes: %t\n", plan.DownVolumes)
	fmt.Fprintf(w, "  down_full: %t\n", plan.DownFull)
	if len(plan.ValidationWarnings) == 0 {
		fmt.Fprintln(w, "validation: ok")
		return
	}
	fmt.Fprintln(w, "validation:")
	fmt.Fprintln(w, "  warnings:")
	for _, warning := range plan.ValidationWarnings {
		fmt.Fprintf(w, "    - %s\n", warning)
	}
}

func yamlValueOrNull(value string) string {
	if value == "" {
		return "null"
	}
	return value
}
