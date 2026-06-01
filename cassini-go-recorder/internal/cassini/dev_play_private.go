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

	fmt.Fprintln(stderr, "play-private playback is not implemented yet; run --scaffold-only for this slice")
	return 2
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
  --conversation      Private playback target. Implemented in later slices; valid values are synthetic and admin.
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

func (c devPlayPrivateOCSClient) do(ctx context.Context, method, path string, form url.Values) (json.RawMessage, error) {
	var body io.Reader
	contentType := ""
	if form != nil {
		body = strings.NewReader(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
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
