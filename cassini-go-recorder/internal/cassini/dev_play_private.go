package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	devPlayPrivateScaffoldStateRel      = "harness/runtime/play-private-scaffold.json"
	devPlayPrivateFallbackPassword      = "CassiniDevPass!2026"
	devPlayPrivateUserPrefix            = "cassini-"
	devPlayPrivateConversationSynthetic = "synthetic"
	devPlayPrivateConversationAdmin     = "admin"
	devPlayPrivateRecordingStatusVideo  = "1"
	devPlayPrivateRecordingActive       = 1
)

var (
	devPlayPrivateRecordingPollInterval = 500 * time.Millisecond
	devPlayPrivateRecordingPollTimeout  = 90 * time.Second
)

type devPlayPrivateOptions struct {
	nextcloudHost   string
	scaffoldOnly    bool
	conversation    string
	durationSeconds int
}

type devPlayPrivateSyntheticUser struct {
	UserID         string `json:"userId"`
	DisplayName    string `json:"displayName"`
	SpeakerID      string `json:"speakerId"`
	PasswordSource string `json:"passwordSource,omitempty"`
}

type devPlayPrivateConversationState struct {
	Token         string `json:"token"`
	CallURL       string `json:"callUrl"`
	CreatorUserID string `json:"creatorUserId"`
	InviteUserID  string `json:"inviteUserId"`
}

type devPlayPrivateScaffoldState struct {
	Version          int       `json:"version"`
	CreatedAt        time.Time `json:"createdAt"`
	BaseURL          string    `json:"baseUrl"`
	CredentialSource string    `json:"credentialSource"`
	Fixture          struct {
		MediaLabel   string `json:"mediaLabel"`
		Scenario     string `json:"scenario"`
		OutputDir    string `json:"outputDir"`
		Participants []struct {
			SpeakerID   string `json:"speakerId"`
			DisplayName string `json:"displayName"`
			UserID      string `json:"userId"`
		} `json:"participants"`
	} `json:"fixture"`
	Users struct {
		Erlich devPlayPrivateSyntheticUser `json:"erlich"`
		Monica devPlayPrivateSyntheticUser `json:"monica"`
		Admin  struct {
			UserID         string `json:"userId"`
			PasswordSource string `json:"passwordSource,omitempty"`
		} `json:"admin"`
	} `json:"users"`
	Conversations map[string]devPlayPrivateConversationState `json:"conversations"`
}

type devPlayPrivateActor struct {
	UserID      string
	Password    string
	DisplayName string
	SpeakerID   string
}

type devPlayPrivateOCSClient struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

type devPlayPrivateOCSError struct {
	HTTPStatus int
	Status     string
	StatusCode int
	Message    string
}

func (e devPlayPrivateOCSError) Error() string {
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("HTTP %d status=%s code=%d message=%q", e.HTTPStatus, e.Status, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("status=%s code=%d message=%q", e.Status, e.StatusCode, e.Message)
}

func runDevPlayPrivate(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	opts := devPlayPrivateOptions{}
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printDevPlayPrivateUsage(stdout)
			return 0
		}
	}

	fs := flag.NewFlagSet("cassini dev play-private", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.nextcloudHost, "nextcloud-host", "", "Nextcloud harness host or base URL (defaults to CASSINI_HARNESS_HOST, then 127.0.0.1)")
	fs.BoolVar(&opts.scaffoldOnly, "scaffold-only", false, "prepare private playback users/conversations and exit")
	fs.StringVar(&opts.conversation, "conversation", "", "private conversation target: synthetic or admin")
	fs.IntVar(&opts.durationSeconds, "duration", 0, "playback duration in seconds (for --conversation playback)")
	fs.Usage = func() { printDevPlayPrivateUsage(fs.Output()) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "play-private does not accept positional arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}
	if err := validateDevPlayPrivateOptions(&opts); err != nil {
		fmt.Fprintf(stderr, "play-private configuration error: %v\n", err)
		fs.Usage()
		return 2
	}

	if opts.scaffoldOnly {
		if err := scaffoldDevPlayPrivate(ctx, repoRoot, opts, stdout); err != nil {
			fmt.Fprintf(stderr, "play-private scaffold error: %v\n", err)
			return 1
		}
		return 0
	}

	if err := runDevPlayPrivateConversation(ctx, repoRoot, opts, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "play-private playback error: %v\n", err)
		return 1
	}
	return 0
}

func validateDevPlayPrivateOptions(opts *devPlayPrivateOptions) error {
	opts.nextcloudHost = strings.TrimSpace(opts.nextcloudHost)
	opts.conversation = strings.TrimSpace(strings.ToLower(opts.conversation))
	if opts.durationSeconds < 0 {
		return errors.New("--duration must be >= 0")
	}
	if opts.scaffoldOnly && opts.conversation != "" {
		return errors.New("--scaffold-only cannot be combined with --conversation")
	}
	if opts.scaffoldOnly {
		return nil
	}
	if opts.conversation == "" {
		return errors.New("provide --scaffold-only or --conversation synthetic|admin")
	}
	switch opts.conversation {
	case devPlayPrivateConversationSynthetic, devPlayPrivateConversationAdmin:
		return nil
	default:
		return fmt.Errorf("--conversation must be %q or %q", devPlayPrivateConversationSynthetic, devPlayPrivateConversationAdmin)
	}
}

func printDevPlayPrivateUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini dev play-private --scaffold-only [--nextcloud-host <host-or-url>]
  cassini dev play-private --conversation synthetic|admin [--nextcloud-host <host-or-url>] [--duration <seconds>]

Examples:
  cassini dev play-private --scaffold-only
  cassini dev play-private --nextcloud-host 192.168.252.21 --scaffold-only

`)
	fmt.Fprintf(w, `Options:
  --scaffold-only     Create/reuse private playback users and 1:1 Talk conversations, then exit.
  --conversation      Private playback target. synthetic is implemented now; admin lands in the next slice.
  --nextcloud-host    Bare host/IP or full base URL. Defaults to CASSINI_HARNESS_HOST, then 127.0.0.1.
  --duration          Seconds to play for --conversation playback.

Scaffold credentials:
  Synthetic users use CASSINI_PLAY_SCAFFOLD_PASSWORD when set. If unset, the local dev fallback %q is used.
`, devPlayPrivateFallbackPassword)
	fmt.Fprintln(w)
}

func scaffoldDevPlayPrivate(ctx context.Context, repoRoot string, opts devPlayPrivateOptions, stdout io.Writer) error {
	baseURL, err := normalizeDevPlayBaseURL(opts.nextcloudHost, os.Getenv("CASSINI_HARNESS_HOST"))
	if err != nil {
		return err
	}
	if code := ensureDevPlayPiedPiperFixture(ctx, repoRoot, stdout, io.Discard); code != 0 {
		return fmt.Errorf("prepare Pied Piper fixture exited with code %d", code)
	}

	password, passwordSource := devPlayPrivateScaffoldPassword()
	adminUser := envOrDefault("ADMIN_USER", "admin")
	adminPassword := envOrDefault("ADMIN_PASSWORD", "admin")
	erlich := devPlayPrivateActor{
		UserID:      devPlayPrivateUserPrefix + devPlayPiedPiperFirstID,
		Password:    password,
		DisplayName: devPlayPiedPiperFirstName,
		SpeakerID:   devPlayPiedPiperFirstID,
	}
	monica := devPlayPrivateActor{
		UserID:      devPlayPrivateUserPrefix + devPlayPiedPiperSecondID,
		Password:    password,
		DisplayName: devPlayPiedPiperSecondName,
		SpeakerID:   devPlayPiedPiperSecondID,
	}

	adminClient := newDevPlayPrivateOCSClient(baseURL, adminUser, adminPassword)
	if err := adminClient.ensureUser(ctx, erlich.UserID, erlich.Password, erlich.DisplayName); err != nil {
		return fmt.Errorf("ensure user %s: %w", erlich.UserID, err)
	}
	if err := adminClient.ensureUser(ctx, monica.UserID, monica.Password, monica.DisplayName); err != nil {
		return fmt.Errorf("ensure user %s: %w", monica.UserID, err)
	}

	erlichClient := newDevPlayPrivateOCSClient(baseURL, erlich.UserID, erlich.Password)
	syntheticToken, err := erlichClient.ensureOneToOneConversation(ctx, monica.UserID)
	if err != nil {
		return fmt.Errorf("ensure synthetic 1:1 conversation: %w", err)
	}
	adminConversationClient := newDevPlayPrivateOCSClient(baseURL, adminUser, adminPassword)
	adminToken, err := adminConversationClient.ensureOneToOneConversation(ctx, erlich.UserID)
	if err != nil {
		return fmt.Errorf("ensure admin 1:1 conversation: %w", err)
	}

	state := buildDevPlayPrivateScaffoldState(repoRoot, baseURL, passwordSource, adminUser, erlich, monica, syntheticToken, adminToken)
	statePath := filepath.Join(repoRoot, devPlayPrivateScaffoldStateRel)
	if err := writeDevPlayPrivateScaffoldState(statePath, state); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "play-private scaffold -> base=%s state=%s\n", baseURL, statePath)
	if passwordSource == "dev-fallback" {
		fmt.Fprintf(stdout, "play-private scaffold -> using dev fallback password; set CASSINI_PLAY_SCAFFOLD_PASSWORD to override\n")
	}
	fmt.Fprintf(stdout, "play-private scaffold -> users=%s,%s conversations synthetic=%s admin=%s\n", erlich.UserID, monica.UserID, syntheticToken, adminToken)
	return nil
}

func devPlayPrivateScaffoldPassword() (string, string) {
	if value := strings.TrimSpace(os.Getenv("CASSINI_PLAY_SCAFFOLD_PASSWORD")); value != "" {
		return value, "env:CASSINI_PLAY_SCAFFOLD_PASSWORD"
	}
	return devPlayPrivateFallbackPassword, "dev-fallback"
}

func buildDevPlayPrivateScaffoldState(repoRoot string, baseURL string, credentialSource string, adminUser string, erlich, monica devPlayPrivateActor, syntheticToken, adminToken string) devPlayPrivateScaffoldState {
	state := devPlayPrivateScaffoldState{
		Version:          1,
		CreatedAt:        time.Now().UTC(),
		BaseURL:          strings.TrimRight(baseURL, "/"),
		CredentialSource: credentialSource,
		Conversations: map[string]devPlayPrivateConversationState{
			devPlayPrivateConversationSynthetic: {
				Token:         syntheticToken,
				CallURL:       strings.TrimRight(baseURL, "/") + "/call/" + syntheticToken,
				CreatorUserID: erlich.UserID,
				InviteUserID:  monica.UserID,
			},
			devPlayPrivateConversationAdmin: {
				Token:         adminToken,
				CallURL:       strings.TrimRight(baseURL, "/") + "/call/" + adminToken,
				CreatorUserID: adminUser,
				InviteUserID:  erlich.UserID,
			},
		},
	}
	state.Fixture.MediaLabel = devPlayPiedPiperMediaLabel
	state.Fixture.Scenario = filepath.Join(repoRoot, devPlayPiedPiperScenarioRel)
	state.Fixture.OutputDir = filepath.Join(repoRoot, devPlayPiedPiperOutputRel)
	state.Fixture.Participants = []struct {
		SpeakerID   string `json:"speakerId"`
		DisplayName string `json:"displayName"`
		UserID      string `json:"userId"`
	}{
		{SpeakerID: erlich.SpeakerID, DisplayName: erlich.DisplayName, UserID: erlich.UserID},
		{SpeakerID: monica.SpeakerID, DisplayName: monica.DisplayName, UserID: monica.UserID},
	}
	state.Users.Erlich = devPlayPrivateSyntheticUser{UserID: erlich.UserID, DisplayName: erlich.DisplayName, SpeakerID: erlich.SpeakerID, PasswordSource: credentialSource}
	state.Users.Monica = devPlayPrivateSyntheticUser{UserID: monica.UserID, DisplayName: monica.DisplayName, SpeakerID: monica.SpeakerID, PasswordSource: credentialSource}
	state.Users.Admin.UserID = adminUser
	state.Users.Admin.PasswordSource = "env:ADMIN_PASSWORD|default"
	return state
}

func runDevPlayPrivateConversation(ctx context.Context, repoRoot string, opts devPlayPrivateOptions, stdout, stderr io.Writer) error {
	state, err := readDevPlayPrivateScaffoldState(filepath.Join(repoRoot, devPlayPrivateScaffoldStateRel))
	if err != nil {
		return err
	}
	baseURL, err := resolveDevPlayPrivatePlaybackBaseURL(opts, state)
	if err != nil {
		return err
	}
	if opts.conversation != devPlayPrivateConversationSynthetic {
		return fmt.Errorf("--conversation %s is implemented in a later slice", opts.conversation)
	}
	password, err := devPlayPrivatePasswordForState(state)
	if err != nil {
		return err
	}
	target, err := resolveDevPlayPrivateSyntheticTarget(repoRoot, baseURL, state, password)
	if err != nil {
		return err
	}

	client := newDevPlayPrivateOCSClient(baseURL, target.RecordingStarter.UserID, target.RecordingStarter.Password)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := client.stopRecording(cleanupCtx, target.Token); err != nil {
			fmt.Fprintf(stderr, "play-private cleanup warning: stop recording: %v\n", err)
		}
	}()

	fmt.Fprintf(stdout, "play-private -> conversation=%s call=%s recording=player-gated duration=%s publishers=%s\n", opts.conversation, target.CallURL, devPlayPrivateDurationLabel(opts.durationSeconds), strings.Join(target.PublisherLabels(), ","))
	if code := runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "stream-video.sh"), target.StreamVideoArgs(opts.durationSeconds), stdout, stderr); code != 0 {
		return fmt.Errorf("authenticated media playback exited with code %d", code)
	}
	return nil
}

type devPlayPrivatePlaybackTarget struct {
	Conversation     string
	Token            string
	CallURL          string
	RecordingStarter devPlayPrivateActor
	MediaPublishers  []devPlayPrivateActor
	MediaPrefixes    []string
}

func (t devPlayPrivatePlaybackTarget) PublisherLabels() []string {
	labels := make([]string, 0, len(t.MediaPublishers))
	for _, publisher := range t.MediaPublishers {
		labels = append(labels, publisher.UserID+":"+publisher.SpeakerID)
	}
	return labels
}

func (t devPlayPrivatePlaybackTarget) StreamVideoArgs(durationSeconds int) []string {
	users := make([]string, 0, len(t.MediaPublishers))
	passwords := make([]string, 0, len(t.MediaPublishers))
	names := make([]string, 0, len(t.MediaPublishers))
	for _, publisher := range t.MediaPublishers {
		users = append(users, publisher.UserID)
		passwords = append(passwords, publisher.Password)
		names = append(names, publisher.DisplayName)
	}
	args := []string{
		"--call-url", t.CallURL,
		"--users", strconv.Itoa(len(t.MediaPublishers)),
		"--duration", strconv.Itoa(durationSeconds),
		"--media-prefixes", strings.Join(t.MediaPrefixes, ","),
		"--names", strings.Join(names, ","),
		"--auth-users", strings.Join(users, ","),
		"--auth-passwords", strings.Join(passwords, ","),
		"--record-before-media",
		"--recording-starter-index", "1",
		"--recording-timeout", strconv.Itoa(int(devPlayPrivateRecordingPollTimeout.Seconds())),
		"--skip-prepare",
	}
	return args
}

func readDevPlayPrivateScaffoldState(path string) (devPlayPrivateScaffoldState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return devPlayPrivateScaffoldState{}, fmt.Errorf("missing scaffold state %s; run `cassini dev play-private --scaffold-only`", path)
		}
		return devPlayPrivateScaffoldState{}, fmt.Errorf("read scaffold state: %w", err)
	}
	var state devPlayPrivateScaffoldState
	if err := json.Unmarshal(body, &state); err != nil {
		return devPlayPrivateScaffoldState{}, fmt.Errorf("decode scaffold state: %w", err)
	}
	if state.Version != 1 {
		return devPlayPrivateScaffoldState{}, fmt.Errorf("unsupported scaffold state version %d; rerun `cassini dev play-private --scaffold-only`", state.Version)
	}
	return state, nil
}

func resolveDevPlayPrivatePlaybackBaseURL(opts devPlayPrivateOptions, state devPlayPrivateScaffoldState) (string, error) {
	hasOverride := strings.TrimSpace(opts.nextcloudHost) != "" || strings.TrimSpace(os.Getenv("CASSINI_HARNESS_HOST")) != ""
	if !hasOverride {
		if strings.TrimSpace(state.BaseURL) == "" {
			return "", errors.New("scaffold state missing baseUrl; rerun `cassini dev play-private --scaffold-only`")
		}
		return strings.TrimRight(state.BaseURL, "/"), nil
	}
	baseURL, err := normalizeDevPlayBaseURL(opts.nextcloudHost, os.Getenv("CASSINI_HARNESS_HOST"))
	if err != nil {
		return "", err
	}
	if strings.TrimRight(state.BaseURL, "/") != baseURL {
		return "", fmt.Errorf("scaffold state baseUrl %s does not match requested %s; rerun `cassini dev play-private --scaffold-only --nextcloud-host %s`", state.BaseURL, baseURL, opts.nextcloudHost)
	}
	return baseURL, nil
}

func devPlayPrivatePasswordForState(state devPlayPrivateScaffoldState) (string, error) {
	source := strings.TrimSpace(state.CredentialSource)
	switch source {
	case "dev-fallback", "":
		return devPlayPrivateFallbackPassword, nil
	case "env:CASSINI_PLAY_SCAFFOLD_PASSWORD":
		if value := strings.TrimSpace(os.Getenv("CASSINI_PLAY_SCAFFOLD_PASSWORD")); value != "" {
			return value, nil
		}
		return "", errors.New("scaffold state was created with CASSINI_PLAY_SCAFFOLD_PASSWORD; set it before playback or rerun scaffold")
	default:
		return "", fmt.Errorf("unsupported scaffold credentialSource %q; rerun scaffold", source)
	}
}

func resolveDevPlayPrivateSyntheticTarget(repoRoot string, baseURL string, state devPlayPrivateScaffoldState, password string) (devPlayPrivatePlaybackTarget, error) {
	conversation, ok := state.conversation(devPlayPrivateConversationSynthetic)
	if !ok || strings.TrimSpace(conversation.Token) == "" {
		return devPlayPrivatePlaybackTarget{}, errors.New("scaffold state missing synthetic conversation; rerun `cassini dev play-private --scaffold-only`")
	}
	outputDir := strings.TrimSpace(state.Fixture.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(repoRoot, devPlayPiedPiperOutputRel)
	}
	publishers := []devPlayPrivateActor{
		{UserID: state.Users.Erlich.UserID, Password: password, DisplayName: state.Users.Erlich.DisplayName, SpeakerID: state.Users.Erlich.SpeakerID},
		{UserID: state.Users.Monica.UserID, Password: password, DisplayName: state.Users.Monica.DisplayName, SpeakerID: state.Users.Monica.SpeakerID},
	}
	for i, publisher := range publishers {
		if strings.TrimSpace(publisher.UserID) == "" || strings.TrimSpace(publisher.DisplayName) == "" || strings.TrimSpace(publisher.SpeakerID) == "" {
			return devPlayPrivatePlaybackTarget{}, fmt.Errorf("scaffold state missing synthetic publisher %d fields; rerun scaffold", i+1)
		}
	}
	mediaPrefixes := []string{
		filepath.Join(outputDir, publishers[0].SpeakerID),
		filepath.Join(outputDir, publishers[1].SpeakerID),
	}
	for _, prefix := range mediaPrefixes {
		if !devPlayMediaPairExists(prefix) {
			return devPlayPrivatePlaybackTarget{}, fmt.Errorf("missing media for %s; run `cassini dev play --room <room> --mode single` or rerun scaffold", prefix)
		}
	}
	callURL := strings.TrimSpace(conversation.CallURL)
	if callURL == "" {
		callURL = strings.TrimRight(baseURL, "/") + "/call/" + conversation.Token
	}
	return devPlayPrivatePlaybackTarget{
		Conversation:     devPlayPrivateConversationSynthetic,
		Token:            conversation.Token,
		CallURL:          callURL,
		RecordingStarter: publishers[0],
		MediaPublishers:  publishers,
		MediaPrefixes:    mediaPrefixes,
	}, nil
}

func writeDevPlayPrivateScaffoldState(path string, state devPlayPrivateScaffoldState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create scaffold state dir: %w", err)
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode scaffold state: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write scaffold state: %w", err)
	}
	return nil
}

func newDevPlayPrivateOCSClient(baseURL, username, password string) devPlayPrivateOCSClient {
	jar, _ := cookiejar.New(nil)
	return devPlayPrivateOCSClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		},
	}
}

func (c devPlayPrivateOCSClient) ensureUser(ctx context.Context, userID, password, displayName string) error {
	exists, err := c.userExists(ctx, userID)
	if err != nil {
		return err
	}
	if !exists {
		form := url.Values{}
		form.Set("userid", userID)
		form.Set("password", password)
		form.Set("displayName", displayName)
		if _, err := c.do(ctx, http.MethodPost, "/ocs/v1.php/cloud/users", form); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		return nil
	}
	if err := c.updateUserField(ctx, userID, "displayname", displayName); err != nil {
		return fmt.Errorf("update display name: %w", err)
	}
	if err := c.updateUserField(ctx, userID, "password", password); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

func (c devPlayPrivateOCSClient) userExists(ctx context.Context, userID string) (bool, error) {
	_, err := c.do(ctx, http.MethodGet, "/ocs/v1.php/cloud/users/"+url.PathEscape(userID), nil)
	if err == nil {
		return true, nil
	}
	var ocsErr devPlayPrivateOCSError
	if errors.As(err, &ocsErr) && (ocsErr.HTTPStatus == http.StatusNotFound || ocsErr.StatusCode == http.StatusNotFound) {
		return false, nil
	}
	return false, err
}

func (c devPlayPrivateOCSClient) updateUserField(ctx context.Context, userID, key, value string) error {
	form := url.Values{}
	form.Set("key", key)
	form.Set("value", value)
	_, err := c.do(ctx, http.MethodPut, "/ocs/v1.php/cloud/users/"+url.PathEscape(userID), form)
	return err
}

func (c devPlayPrivateOCSClient) ensureOneToOneConversation(ctx context.Context, inviteUserID string) (string, error) {
	form := url.Values{}
	form.Set("roomType", "1")
	form.Set("invite", inviteUserID)
	raw, err := c.do(ctx, http.MethodPost, "/ocs/v2.php/apps/spreed/api/v4/room", form)
	if err != nil {
		return "", err
	}
	if token := tokenFromDevPlayRaw(raw); token != "" {
		return token, nil
	}
	return "", errors.New("response did not include a room token")
}

func (c devPlayPrivateOCSClient) markParticipantActive(ctx context.Context, roomToken string, force bool) (string, error) {
	form := url.Values{}
	form.Set("force", strconv.FormatBool(force))
	raw, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), form)
	if err != nil {
		return "", err
	}
	var data struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("decode participants/active response: %w", err)
	}
	if strings.TrimSpace(data.SessionID) == "" {
		return "", errors.New("participants/active response missing sessionId")
	}
	return data.SessionID, nil
}

func (c devPlayPrivateOCSClient) joinCall(ctx context.Context, roomToken string, flags int, recordingConsent bool) error {
	payload := map[string]any{
		"flags":            flags,
		"silent":           false,
		"recordingConsent": recordingConsent,
		"silentFor":        []any{},
	}
	_, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), payload)
	return err
}

func (c devPlayPrivateOCSClient) leaveCall(ctx context.Context, roomToken string) error {
	_, err := c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/call/%s", roomToken), map[string]any{"all": false})
	return err
}

func (c devPlayPrivateOCSClient) leaveParticipantActive(ctx context.Context, roomToken string) error {
	_, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s/participants/active", roomToken), nil)
	return err
}

func (c devPlayPrivateOCSClient) startRecording(ctx context.Context, roomToken string) error {
	form := url.Values{}
	form.Set("status", devPlayPrivateRecordingStatusVideo)
	_, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/recording/%s", roomToken), form)
	return err
}

func (c devPlayPrivateOCSClient) stopRecording(ctx context.Context, roomToken string) error {
	_, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v1/recording/%s", roomToken), nil)
	return err
}

func (c devPlayPrivateOCSClient) roomRecordingState(ctx context.Context, roomToken string) (int, error) {
	raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/ocs/v2.php/apps/spreed/api/v4/room/%s", roomToken), nil)
	if err != nil {
		return 0, err
	}
	var data struct {
		CallRecording any `json:"callRecording"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, fmt.Errorf("decode room state: %w", err)
	}
	state, ok := intFromAny(data.CallRecording)
	if !ok {
		return 0, fmt.Errorf("room state missing numeric callRecording: %v", data.CallRecording)
	}
	return state, nil
}

func waitDevPlayPrivateRecordingActive(ctx context.Context, client devPlayPrivateOCSClient, roomToken string) error {
	pollCtx, cancel := context.WithTimeout(ctx, devPlayPrivateRecordingPollTimeout)
	defer cancel()
	for {
		state, err := client.roomRecordingState(pollCtx, roomToken)
		if err != nil {
			return fmt.Errorf("poll recording state: %w", err)
		}
		if state == devPlayPrivateRecordingActive {
			return nil
		}
		timer := time.NewTimer(devPlayPrivateRecordingPollInterval)
		select {
		case <-pollCtx.Done():
			timer.Stop()
			return fmt.Errorf("recording did not become active before timeout; last callRecording=%d", state)
		case <-timer.C:
		}
	}
}

func (c devPlayPrivateOCSClient) doJSON(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal JSON payload: %w", err)
	}
	return c.doWithBody(ctx, method, path, strings.NewReader(string(body)), "application/json")
}

func (c devPlayPrivateOCSClient) do(ctx context.Context, method, path string, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	return c.doWithBody(ctx, method, path, body, contentType)
}

func (c devPlayPrivateOCSClient) doWithBody(ctx context.Context, method, path string, body io.Reader, contentType string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("OCS-APIRequest", "true")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.http.Do(req)
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
	if resp.StatusCode >= 400 || meta.Status != "ok" {
		return nil, devPlayPrivateOCSError{HTTPStatus: resp.StatusCode, Status: meta.Status, StatusCode: meta.StatusCode, Message: meta.Message}
	}
	return envelope.OCS.Data, nil
}

func (s devPlayPrivateScaffoldState) conversation(name string) (devPlayPrivateConversationState, bool) {
	conv, ok := s.Conversations[name]
	return conv, ok
}

func devPlayPrivateDurationLabel(seconds int) string {
	if seconds <= 0 {
		return "whole"
	}
	return strconv.Itoa(seconds) + "s"
}

func intFromAny(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}
