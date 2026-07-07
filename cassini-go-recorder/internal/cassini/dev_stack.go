package cassini

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
)

type devStackPlan struct {
	Command               string
	PublicMode            string
	PublicURL             string
	PublicHost            string
	MediaHost             string
	SignalingPublicURL    string
	ServiceMode           string
	SpreedProfile         string
	CassiniMode           string
	RecordingBackend      string
	ExAppImageMode        string
	PatchMode             string
	ExistingResourceMode  string
	StopFull              bool
	DownVolumes           bool
	BuildRequested        bool
	RemoteConfigRequested bool
	ValidationWarnings    []string
}

type devStackFlagOptions struct {
	publicMode         string
	publicURL          string
	publicHost         string
	mediaHost          string
	signalingPublicURL string
	serviceMode        string
	cassiniMode        string
	recordingBackend   string
	exAppImageMode     string
	patchMode          string
	build              bool
	resume             bool
	reset              bool
	stopFull           bool
	downVolumes        bool
	set                map[string]bool
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
	services := stringFlag("services", "service topology: legacy-default, core, appapi, full, full-remote")
	serviceMode := stringFlag("service-mode", "alias for --services")
	cassiniMode := stringFlag("cassini", "Cassini install mode: none, installed-exapp")
	recordingBackend := stringFlag("recording-backend", "Talk recording backend: legacy, direct-operator, installed-exapp, none")
	exAppImageMode := stringFlag("exapp-image-mode", "ExApp image mode: build, reuse-local, pull")
	patchMode := stringFlag("patch", "patch mode: auto, none, force")
	build := fs.Bool("build", false, "build the Cassini ExApp image before registration")
	resume := fs.Bool("resume", false, "resume matching stopped resources instead of creating a fresh stack")
	reset := fs.Bool("reset", false, "stop/remove/recreate resources for the resolved stack")
	stopFull := fs.Bool("full", false, "for stack stop: remove all harness-owned resources")
	downVolumes := fs.Bool("volumes", false, "for stack down: remove volumes (compatibility alias for full teardown of current config)")

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
	opts.serviceMode = *services
	opts.cassiniMode = *cassiniMode
	opts.recordingBackend = *recordingBackend
	opts.exAppImageMode = *exAppImageMode
	opts.patchMode = *patchMode
	opts.build = *build
	opts.resume = *resume
	opts.reset = *reset
	opts.stopFull = *stopFull
	opts.downVolumes = *downVolumes
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
	plan.ServiceMode = pick("services", opts.serviceMode, "CASSINI_HARNESS_SERVICE_MODE", "")
	plan.CassiniMode = pick("cassini", opts.cassiniMode, "CASSINI_HARNESS_CASSINI_MODE", devStackCassiniNone)
	plan.RecordingBackend = pick("recording-backend", opts.recordingBackend, "CASSINI_HARNESS_RECORDING_BACKEND", devStackRecordingLegacy)
	plan.ExAppImageMode = pick("exapp-image-mode", opts.exAppImageMode, "CASSINI_HARNESS_EXAPP_IMAGE_MODE", devStackImageReuseLocal)
	plan.PatchMode = pick("patch", opts.patchMode, "CASSINI_HARNESS_PATCH_MODE", devStackPatchAuto)
	plan.StopFull = opts.stopFull
	plan.DownVolumes = opts.downVolumes
	plan.BuildRequested = opts.build

	if opts.build {
		plan.ExAppImageMode = devStackImageBuild
	}
	if command != "up" && (opts.resume || opts.reset) {
		return plan, rest, errors.New("--resume and --reset apply only to stack up")
	}
	if command != "down" && (opts.stopFull || opts.downVolumes) {
		return plan, rest, errors.New("--full and --volumes apply only to stack stop/down")
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
	return plan, rest, nil
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
	if plan.PublicMode == devStackPublicLANHTTP && plan.PublicURL != "" {
		if scheme, ok := devStackURLScheme(plan.PublicURL); !ok || scheme != "http" {
			return fmt.Errorf("lan-http public URL must be http, got %q", plan.PublicURL)
		}
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
		"SPREED_PROFILE=" + plan.SpreedProfile,
		"CASSINI_HARNESS_PUBLIC_URL=" + plan.PublicURL,
		"CASSINI_HARNESS_PUBLIC_HOST=" + plan.PublicHost,
		"CASSINI_HARNESS_MEDIA_HOST=" + plan.MediaHost,
		"CASSINI_HARNESS_SIGNALING_PUBLIC_URL=" + plan.SignalingPublicURL,
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
	fmt.Fprintln(w, "patch:")
	fmt.Fprintf(w, "  mode: %s\n", plan.PatchMode)
	fmt.Fprintln(w, "lifecycle:")
	fmt.Fprintf(w, "  existing_resources: %s\n", plan.ExistingResourceMode)
	fmt.Fprintf(w, "  stop_full: %t\n", plan.StopFull)
	fmt.Fprintf(w, "  down_volumes: %t\n", plan.DownVolumes)
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
