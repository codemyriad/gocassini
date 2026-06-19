package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	devPlayDefaultHost          = "127.0.0.1"
	devPlayDefaultPort          = "28080"
	devPlayDefaultMode          = "full"
	devPlaySingleMode           = "single"
	devPlayFullMode             = "full"
	devPlayPiedPiperMediaLabel  = "synthetic-pied-piper-v1"
	devPlayPiedPiperScenarioRel = "harness/scenarios/synthetic-pied-piper.v1.json"
	devPlayPiedPiperOutputRel   = "harness/media/processed/synthetic-pied-piper-v1"
	devPlayPiedPiperFirstID     = "erlich"
	devPlayPiedPiperFirstName   = "Erlich Bachman"
	devPlayPiedPiperSecondID    = "monica"
	devPlayPiedPiperSecondName  = "Monica Hall"
)

var devPlayHTTPClient = &http.Client{Timeout: 20 * time.Second}

type devPlayOptions struct {
	nextcloudHost   string
	roomName        string
	mode            string
	durationSeconds int
}

type devPlayTarget struct {
	BaseURL   string
	RoomName  string
	RoomToken string
	CallURL   string
	Created   bool
}

type devPlayTalkRoom struct {
	Token       string
	DisplayName string
}

type devPlayOCSEnvelope struct {
	OCS struct {
		Meta struct {
			Status     string `json:"status"`
			StatusCode int    `json:"statuscode"`
			Message    string `json:"message"`
		} `json:"meta"`
		Data json.RawMessage `json:"data"`
	} `json:"ocs"`
}

func runDevPlay(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	opts := devPlayOptions{mode: devPlayDefaultMode}

	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printDevPlayUsage(stdout)
			return 0
		}
	}

	fs := flag.NewFlagSet("cassini dev play", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.nextcloudHost, "nextcloud-host", "", "Nextcloud harness host or base URL (defaults to CASSINI_HARNESS_HOST, then 127.0.0.1)")
	fs.StringVar(&opts.roomName, "room", "", "required Talk room display name; created when missing")
	fs.StringVar(&opts.mode, "mode", devPlayDefaultMode, "playback mode: single or full")
	fs.IntVar(&opts.durationSeconds, "duration", 0, "playback duration in seconds (0 or omitted plays the whole selected clip/fixture)")
	fs.Usage = func() {
		printDevPlayUsage(fs.Output())
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "play does not accept positional arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}
	if err := validateDevPlayOptions(&opts); err != nil {
		fmt.Fprintf(stderr, "play configuration error: %v\n", err)
		fs.Usage()
		return 2
	}

	target, err := resolveDevPlayTarget(ctx, repoRoot, opts)
	if err != nil {
		fmt.Fprintf(stderr, "play target error: %v\n", err)
		return 1
	}

	if code := ensureDevPlayPiedPiperFixture(ctx, repoRoot, stdout, stderr); code != 0 {
		return code
	}

	relScript, scriptArgs := devPlayScriptInvocation(repoRoot, target.CallURL, opts)
	durationLabel := "whole"
	if opts.durationSeconds > 0 {
		durationLabel = strconv.Itoa(opts.durationSeconds) + "s"
	}
	createdLabel := "existing"
	if target.Created {
		createdLabel = "created"
	}
	fmt.Fprintf(stdout, "play -> room=%q room_state=%s call=%s mode=%s duration=%s media=%s\n", target.RoomName, createdLabel, target.CallURL, opts.mode, durationLabel, devPlayPiedPiperMediaLabel)

	extraEnv := []string{
		"CALL_URL=" + target.CallURL,
		"NEXTCLOUD_URL=" + target.BaseURL,
		"NEXTCLOUD_STATUS_URL=" + target.BaseURL + "/status.php",
		"PREPARE=0",
	}
	return runDevScriptWithEnv(ctx, repoRoot, relScript, scriptArgs, extraEnv, stdout, stderr)
}

func validateDevPlayOptions(opts *devPlayOptions) error {
	opts.roomName = strings.TrimSpace(opts.roomName)
	opts.nextcloudHost = strings.TrimSpace(opts.nextcloudHost)
	opts.mode = strings.TrimSpace(strings.ToLower(opts.mode))
	if opts.roomName == "" {
		return errors.New("--room is required")
	}
	switch opts.mode {
	case devPlaySingleMode, devPlayFullMode:
	default:
		return fmt.Errorf("--mode must be %q or %q", devPlaySingleMode, devPlayFullMode)
	}
	if opts.durationSeconds < 0 {
		return errors.New("--duration must be >= 0")
	}
	return nil
}

func printDevPlayUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini dev play --room <name> [--nextcloud-host <host-or-url>] [--mode single|full] [--duration <seconds>]

Examples:
  cassini dev play --room "Local smoke room"
  cassini dev play --room "Local smoke room" --mode single --duration 20
  cassini dev play --nextcloud-host 192.168.252.21 --room "VM smoke room" --duration 20

`)
	fmt.Fprint(w, `Options:
  --room             Required Talk room display name. Existing rooms are reused; missing rooms are created.
  --nextcloud-host   Bare host/IP or full base URL. Defaults to CASSINI_HARNESS_HOST, then 127.0.0.1.
  --mode             single or full. Defaults to full.
  --duration         Seconds to play. Omit or pass 0 to play the whole selected clip/fixture.
`+"\n")
}

func resolveDevPlayTarget(ctx context.Context, repoRoot string, opts devPlayOptions) (devPlayTarget, error) {
	baseURL, err := normalizeDevPlayBaseURL(opts.nextcloudHost, os.Getenv("CASSINI_HARNESS_HOST"))
	if err != nil {
		return devPlayTarget{}, err
	}

	client := devPlayOCSClient{
		baseURL:  baseURL,
		username: envOrDefault("ADMIN_USER", "admin"),
		password: envOrDefault("ADMIN_PASSWORD", "admin"),
		http:     devPlayHTTPClient,
	}
	rooms, err := client.listRooms(ctx)
	if err != nil {
		return devPlayTarget{}, err
	}

	roomName := strings.TrimSpace(opts.roomName)
	var token string
	created := false
	for _, room := range rooms {
		if strings.TrimSpace(room.DisplayName) == roomName && strings.TrimSpace(room.Token) != "" {
			token = strings.TrimSpace(room.Token)
			break
		}
	}
	if token == "" {
		token, err = client.createRoom(ctx, roomName)
		if err != nil {
			return devPlayTarget{}, err
		}
		created = true
	}

	callURL := strings.TrimRight(baseURL, "/") + "/call/" + token
	if err := writeDevPlayRuntimeState(repoRoot, token, callURL); err != nil {
		return devPlayTarget{}, err
	}
	return devPlayTarget{
		BaseURL:   baseURL,
		RoomName:  roomName,
		RoomToken: token,
		CallURL:   callURL,
		Created:   created,
	}, nil
}

func normalizeDevPlayBaseURL(flagValue string, envValue string) (string, error) {
	raw := strings.TrimSpace(flagValue)
	if raw == "" {
		raw = strings.TrimSpace(envValue)
	}
	if raw == "" {
		raw = devPlayDefaultHost
	}
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", fmt.Errorf("invalid --nextcloud-host URL %q", raw)
		}
		return strings.TrimRight(raw, "/"), nil
	}
	if strings.Contains(raw, "/") {
		return "", fmt.Errorf("invalid --nextcloud-host %q: use a bare host/IP or full URL", raw)
	}
	if strings.Contains(raw, ":") {
		return "http://" + raw, nil
	}
	return "http://" + raw + ":" + devPlayDefaultPort, nil
}

func devPlayScriptInvocation(repoRoot string, callURL string, opts devPlayOptions) (string, []string) {
	fixtureOutputDir := filepath.Join(repoRoot, devPlayPiedPiperOutputRel)
	if opts.mode == devPlaySingleMode {
		duration := opts.durationSeconds
		args := []string{
			"--call-url", callURL,
			"--users", "1",
			"--duration", strconv.Itoa(duration),
			"--media-prefix", filepath.Join(fixtureOutputDir, devPlayPiedPiperFirstID),
			"--names", devPlayPiedPiperFirstName,
			"--skip-prepare",
		}
		return filepath.Join("harness", "bin", "stream-video.sh"), args
	}

	args := []string{
		"--call-url", callURL,
		"--scenario", filepath.Join(repoRoot, devPlayPiedPiperScenarioRel),
		"--output-dir", fixtureOutputDir,
		"--skip-prepare",
	}
	if opts.durationSeconds > 0 {
		args = append(args, "--duration", strconv.Itoa(opts.durationSeconds))
	}
	return filepath.Join("harness", "bin", "stream-synthetic-meeting.sh"), args
}

func ensureDevPlayPiedPiperFixture(ctx context.Context, repoRoot string, stdout, stderr io.Writer) int {
	if devPlayPiedPiperFixtureComplete(repoRoot) {
		return 0
	}
	fmt.Fprintf(stdout, "play -> preparing media=%s\n", devPlayPiedPiperMediaLabel)
	args := []string{
		"--scenario", filepath.Join(repoRoot, devPlayPiedPiperScenarioRel),
		"--output-dir", filepath.Join(repoRoot, devPlayPiedPiperOutputRel),
	}
	return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "prepare-synthetic-meeting.sh"), args, stdout, stderr)
}

type devPlaySyntheticManifest struct {
	Participants []struct {
		ID          string `json:"id"`
		MediaPrefix string `json:"media_prefix"`
	} `json:"participants"`
}

func devPlayPiedPiperFixtureComplete(repoRoot string) bool {
	outputDir := filepath.Join(repoRoot, devPlayPiedPiperOutputRel)
	manifestPath := filepath.Join(outputDir, "manifest.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return false
	}
	var manifest devPlaySyntheticManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false
	}
	if len(manifest.Participants) == 0 {
		return false
	}
	for _, participant := range manifest.Participants {
		prefix := devPlayParticipantMediaPrefix(outputDir, participant.ID, participant.MediaPrefix)
		if prefix == "" || !devPlayMediaPairExists(prefix) {
			return false
		}
	}
	return true
}

func devPlayParticipantMediaPrefix(outputDir string, participantID string, manifestPrefix string) string {
	participantID = strings.TrimSpace(participantID)
	manifestPrefix = strings.TrimSpace(manifestPrefix)
	if participantID != "" {
		return filepath.Join(outputDir, participantID)
	}
	if manifestPrefix == "" {
		return ""
	}
	if filepath.IsAbs(manifestPrefix) {
		if devPlayMediaPairExists(manifestPrefix) {
			return manifestPrefix
		}
		return filepath.Join(outputDir, filepath.Base(manifestPrefix))
	}
	return filepath.Join(outputDir, manifestPrefix)
}

func devPlayMediaPairExists(prefix string) bool {
	if _, err := os.Stat(prefix + ".ivf"); err != nil {
		return false
	}
	if _, err := os.Stat(prefix + ".ogg"); err != nil {
		return false
	}
	return true
}

func writeDevPlayRuntimeState(repoRoot string, roomToken string, callURL string) error {
	runtimeDir := filepath.Join(repoRoot, "harness", "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("create harness runtime dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "last_room_token"), []byte(roomToken+"\n"), 0o644); err != nil {
		return fmt.Errorf("write last_room_token: %w", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "last_call_url"), []byte(callURL+"\n"), 0o644); err != nil {
		return fmt.Errorf("write last_call_url: %w", err)
	}
	return nil
}

type devPlayOCSClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func (c devPlayOCSClient) listRooms(ctx context.Context) ([]devPlayTalkRoom, error) {
	raw, err := c.do(ctx, http.MethodGet, "/ocs/v2.php/apps/spreed/api/v4/room", nil)
	if err != nil {
		return nil, fmt.Errorf("list Talk rooms: %w", err)
	}
	return decodeDevPlayRooms(raw), nil
}

func (c devPlayOCSClient) createRoom(ctx context.Context, roomName string) (string, error) {
	form := url.Values{}
	form.Set("roomType", "3")
	form.Set("roomName", roomName)
	raw, err := c.do(ctx, http.MethodPost, "/ocs/v2.php/apps/spreed/api/v4/room", form)
	if err != nil {
		return "", fmt.Errorf("create Talk room %q: %w", roomName, err)
	}
	token := tokenFromDevPlayRaw(raw)
	if token == "" {
		return "", fmt.Errorf("create Talk room %q: response did not include a room token", roomName)
	}
	return token, nil
}

func (c devPlayOCSClient) do(ctx context.Context, method string, path string, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.baseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	httpClient := c.http
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var envelope devPlayOCSEnvelope
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return nil, fmt.Errorf("decode OCS response: %w", err)
	}
	meta := envelope.OCS.Meta
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d status=%s code=%d message=%q", resp.StatusCode, meta.Status, meta.StatusCode, meta.Message)
	}
	if meta.Status != "ok" {
		return nil, fmt.Errorf("status=%s code=%d message=%q", meta.Status, meta.StatusCode, meta.Message)
	}
	return envelope.OCS.Data, nil
}

func decodeDevPlayRooms(raw json.RawMessage) []devPlayTalkRoom {
	var rooms []devPlayTalkRoom
	appendRoom := func(item map[string]any) {
		token := stringFieldFromMap(item, "token", "roomToken", "id")
		displayName := stringFieldFromMap(item, "displayName", "name", "roomName", "title", "conversationName")
		if token == "" || displayName == "" {
			return
		}
		rooms = append(rooms, devPlayTalkRoom{Token: token, DisplayName: displayName})
	}

	var arrayItems []map[string]any
	if err := json.Unmarshal(raw, &arrayItems); err == nil {
		for _, item := range arrayItems {
			appendRoom(item)
		}
		return rooms
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return rooms
	}
	if nested, ok := object["rooms"]; ok {
		if nestedBytes, err := json.Marshal(nested); err == nil {
			var nestedRooms []map[string]any
			if err := json.Unmarshal(nestedBytes, &nestedRooms); err == nil {
				for _, item := range nestedRooms {
					appendRoom(item)
				}
				return rooms
			}
		}
	}
	appendRoom(object)
	return rooms
}

func tokenFromDevPlayRaw(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	return stringFieldFromMap(object, "token", "roomToken", "id")
}

func stringFieldFromMap(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			switch typed := value.(type) {
			case string:
				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					return trimmed
				}
			case fmt.Stringer:
				if trimmed := strings.TrimSpace(typed.String()); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func envOrDefault(name string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
