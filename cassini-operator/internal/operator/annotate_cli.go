package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// The operator's side of `cassini annotate`'s contract (D-737): flags in, one
// JSON document out, and an exit code that means something. The two are
// separate modules, so the operator runs the CLI; change both sides together.

const annotateResultFormat = "cassini.annotate.result.v1"

// Anything else non-zero is a runtime failure.
const (
	annotateExitRuntime    = 1
	annotateExitUsage      = 2
	annotateExitRevision   = 3 // --expect-revision did not match
	annotateExitInvalid    = 4 // the ops, or the document they would produce, are invalid
	annotateExitUnresolved = 5 // the marks are bound to different audio; apply refuses
)

const maxAnnotateStderr = 4 << 10

// annotateResult is what every `cassini annotate … --json` prints.
type annotateResult struct {
	Format string `json:"format"`
	// Annotations is the document the file now carries; null when none.
	Annotations     json.RawMessage `json:"annotations"`
	Revision        int             `json:"revision"`
	OperationID     string          `json:"operationId,omitempty"`
	Added           []string        `json:"added,omitempty"`
	Removed         []string        `json:"removed,omitempty"`
	NotFound        []string        `json:"notFound,omitempty"`
	Carried         int             `json:"carried,omitempty"`
	Resolved        *bool           `json:"resolved"`
	AudioOpusSHA256 string          `json:"audioOpusSha256"`
	// ContainerSHA256 is the digest of the file as written — what the bytes are
	// now. Never identity, never compared with the seal.
	ContainerSHA256 string `json:"containerSha256"`
}

// annotateCLIError is a non-zero exit. Message, the CLI's bounded complaint, is
// safe to show a caller only for annotateExitInvalid; otherwise it may name
// local paths.
type annotateCLIError struct {
	Code    int
	Message string
}

func (e *annotateCLIError) Error() string {
	return fmt.Sprintf("cassini annotate exited %d: %s", e.Code, e.Message)
}

// annotateExitCode is the CLI's exit code for err, or 0 when err is not one.
func annotateExitCode(err error) int {
	var cliErr *annotateCLIError
	if errors.As(err, &cliErr) {
		return cliErr.Code
	}
	return 0
}

// annotateApplyOptions are apply's flags. ActorID is always the authenticated
// caller — never a value taken from a request body.
type annotateApplyOptions struct {
	ActorID        string
	ActorKind      string
	OperationID    string
	TagNamespace   string
	ExpectRevision *int
}

// runAnnotateShow reports what path carries, without changing it.
func runAnnotateShow(ctx context.Context, bin, path string) (annotateResult, error) {
	return runAnnotate(ctx, bin, nil, "show", path, "--json")
}

// runAnnotateApply applies ops (a `{"ops":[…]}` document, sent on stdin) to in
// and writes the result to out. in and out may be the same path.
func runAnnotateApply(ctx context.Context, bin, in, out string, ops []byte, opts annotateApplyOptions) (annotateResult, error) {
	if strings.TrimSpace(opts.ActorID) == "" {
		return annotateResult{}, errors.New("annotate apply needs the caller's identity")
	}
	args := []string{"apply", in, "--ops", "-", "--actor-id", opts.ActorID}
	if opts.ActorKind != "" {
		args = append(args, "--actor-kind", opts.ActorKind)
	}
	if opts.OperationID != "" {
		args = append(args, "--operation-id", opts.OperationID)
	}
	if opts.TagNamespace != "" {
		args = append(args, "--tag-namespace", opts.TagNamespace)
	}
	if opts.ExpectRevision != nil {
		args = append(args, "--expect-revision", strconv.Itoa(*opts.ExpectRevision))
	}
	args = append(args, "--out", out, "--json")
	return runAnnotate(ctx, bin, ops, args...)
}

// runAnnotateCarry writes delivered's marks into a copy of sealed at out. The
// sealed file itself is never modified.
func runAnnotateCarry(ctx context.Context, bin, delivered, sealed, out string) (annotateResult, error) {
	return runAnnotate(ctx, bin, nil, "carry", delivered, sealed, "--out", out, "--json")
}

func runAnnotate(ctx context.Context, bin string, stdin []byte, args ...string) (annotateResult, error) {
	if strings.TrimSpace(bin) == "" {
		return annotateResult{}, errors.New("no cassini binary is configured")
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"annotate"}, args...)...)
	// Not os.Environ(): any logged-in caller can make the operator run this, and
	// a child holding APP_SECRET can act as any account (D-700).
	cmd.Env = contextChildEnv(os.Environ())
	// So no ffmpeg grandchild outlives an abandoned request.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxAnnotateStderr {
			msg = msg[:maxAnnotateStderr]
		}
		return annotateResult{}, &annotateCLIError{Code: exitErr.ExitCode(), Message: msg}
	}
	if err != nil {
		return annotateResult{}, fmt.Errorf("run cassini annotate: %w", err)
	}
	var result annotateResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return annotateResult{}, fmt.Errorf("read cassini annotate result: %w", err)
	}
	if result.Format != annotateResultFormat {
		return annotateResult{}, fmt.Errorf("cassini annotate answered format %q, want %q", result.Format, annotateResultFormat)
	}
	return result, nil
}
