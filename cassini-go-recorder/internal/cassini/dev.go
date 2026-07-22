package cassini

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runDev(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printDevUsage(stdout)
		return 0
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(stderr, "dev failed: %v\n", err)
		return 1
	}

	switch args[0] {
	case "help", "-h", "--help":
		printDevUsage(stdout)
		return 0
	case "stack":
		return runDevStack(ctx, repoRoot, args[1:], stdout, stderr)
	case "room":
		return runDevRoom(ctx, repoRoot, args[1:], stdout, stderr)
	case "smoke":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "smoke.sh"), args[1:], stdout, stderr)
	case "ci-e2e":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "ci-e2e.sh"), args[1:], stdout, stderr)
	case "fixture":
		return runDevFixture(ctx, repoRoot, args[1:], stdout, stderr)
	case "play":
		return runDevPlay(ctx, repoRoot, args[1:], stdout, stderr)
	case "play-private":
		return runDevPlayPrivate(ctx, repoRoot, args[1:], stdout, stderr)
	case "player":
		return runDevPlayer(ctx, repoRoot, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev command %q\n\n", args[0])
		printDevUsage(stderr)
		return 2
	}
}

func runDevStack(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printDevStackUsage(stderr)
		return 2
	}

	command := args[0]
	if command == "help" || command == "-h" || command == "--help" {
		printDevStackUsage(stdout)
		return 0
	}

	if command == "stop" {
		fmt.Fprintln(stderr, "dev stack stop was removed; use 'cassini dev stack down' (see 'down --suspend' to keep containers for resume)")
		printDevStackUsage(stderr)
		return 2
	}

	plan, remainingArgs, err := resolveDevStackPlan(command, args[1:], osEnvLookup)
	if err != nil {
		fmt.Fprintf(stderr, "dev stack %s: %v\n", command, err)
		return 2
	}

	if command == "plan" {
		if len(remainingArgs) > 0 {
			fmt.Fprintf(stderr, "dev stack plan: unexpected arguments: %s\n", strings.Join(remainingArgs, " "))
			return 2
		}
		printDevStackPlan(stdout, plan)
		return 0
	}

	harnessBin := filepath.Join("harness", "bin")

	switch command {
	case "up":
		printDevStackCommandWarnings(stderr, command, plan.ValidationWarnings)
		return runDevScriptWithEnv(ctx, repoRoot, filepath.Join(harnessBin, "up.sh"), remainingArgs, plan.env(), stdout, stderr)
	case "down":
		printDevStackCommandWarnings(stderr, command, plan.ValidationWarnings)
		// down is the canonical teardown. Bare down removes the ephemeral
		// containers but keeps volumes (persistence); flags widen or narrow
		// that: --suspend keeps containers, --volumes drops volumes too,
		// --full removes all harness-owned resources.
		scriptArgs := remainingArgs
		switch {
		case plan.DownFull:
			scriptArgs = append([]string{"--full"}, scriptArgs...)
		case plan.DownVolumes:
			scriptArgs = append([]string{"--volumes"}, scriptArgs...)
		case plan.DownSuspend:
			scriptArgs = append([]string{"--suspend"}, scriptArgs...)
		}
		return runDevScriptWithEnv(ctx, repoRoot, filepath.Join(harnessBin, "down.sh"), scriptArgs, plan.env(), stdout, stderr)
	case "status":
		return runDevScriptWithEnv(ctx, repoRoot, filepath.Join("harness", "bin", "status.sh"), remainingArgs, plan.env(), stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev stack command %q\n", args[0])
		printDevStackUsage(stderr)
		return 2
	}
}

func printDevStackCommandWarnings(w io.Writer, command string, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "dev stack %s: validation warnings:\n", command)
	for _, warning := range warnings {
		fmt.Fprintf(w, "  - %s\n", warning)
	}
}

func printDevStackUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini dev stack plan [options]
  cassini dev stack up [options]
  cassini dev stack status [options]
  cassini dev stack down [options]

Common options:
  --public-mode local-http|lan-http|remote-https
  --public-url URL
  --public-host HOST
  --media-host HOST_OR_IP
  --signaling-public-url URL
  --services legacy-default|core|appapi|full|full-remote
  --cassini none|installed-exapp
  --recording-backend legacy|direct-operator|installed-exapp|none
  --exapp-image-mode build|reuse-local|pull
  --build
  --patch=auto|none|force

up options:
  --resume
  --reset

down options (canonical teardown; containers are ephemeral, volumes persist):
  (none)      remove containers, keep volumes (persistence)
  --suspend   stop containers but keep them for 'up --resume'
  --volumes   remove containers and volumes for the current config
  --full      remove all harness-owned resources, including volumes and ExApp

Config hierarchy: explicit flag > CASSINI_HARNESS_* env var > default.
Passing an explicit non-remote --public-mode ignores ambient remote env vars
(CASSINI_HARNESS_PUBLIC_URL/PUBLIC_HOST/MEDIA_HOST/SIGNALING_PUBLIC_URL);
without the explicit flag they still fail loud outside remote-https mode.
`+"\n")
}

func runDevRoom(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: cassini dev room create [args]\n")
		return 2
	}
	switch args[0] {
	case "create":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "create-room.sh"), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev room command %q\n", args[0])
		return 2
	}
}

func runDevFixture(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: cassini dev fixture <prepare-showcase|stream-showcase> [args]\n")
		return 2
	}

	scenario := filepath.Join(repoRoot, "harness", "scenarios", "showcase-lantern-festival.v1.json")
	outputDir := filepath.Join(repoRoot, "harness", "media", "processed", "showcase-lantern-festival-v1")
	switch args[0] {
	case "prepare-showcase":
		base := []string{"--scenario", scenario, "--output-dir", outputDir}
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "prepare-synthetic-meeting.sh"), append(base, args[1:]...), stdout, stderr)
	case "stream-showcase":
		base := []string{"--scenario", scenario, "--output-dir", outputDir}
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "stream-synthetic-meeting.sh"), append(base, args[1:]...), stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev fixture command %q\n", args[0])
		return 2
	}
}

func runDevPlayer(ctx context.Context, repoRoot string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "usage: cassini dev player <video|showcase|three-songs> [args]\n")
		return 2
	}
	switch args[0] {
	case "video":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "stream-video.sh"), args[1:], stdout, stderr)
	case "showcase":
		return runDevFixture(ctx, repoRoot, append([]string{"stream-showcase"}, args[1:]...), stdout, stderr)
	case "three-songs":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "stream-three-songs.sh"), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev player command %q\n", args[0])
		return 2
	}
}

var runDevScriptExec = runDevScriptExecDefault

func runDevScript(ctx context.Context, repoRoot string, relativeScript string, args []string, stdout, stderr io.Writer) int {
	return runDevScriptWithEnv(ctx, repoRoot, relativeScript, args, nil, stdout, stderr)
}

func runDevScriptWithEnv(ctx context.Context, repoRoot string, relativeScript string, args []string, extraEnv []string, stdout, stderr io.Writer) int {
	return runDevScriptExec(ctx, repoRoot, relativeScript, args, extraEnv, stdout, stderr)
}

func runDevScriptExecDefault(ctx context.Context, repoRoot string, relativeScript string, args []string, extraEnv []string, stdout, stderr io.Writer) int {
	scriptPath := filepath.Join(repoRoot, relativeScript)
	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = os.Stdin
	cmd.Env = append(os.Environ(), extraEnv...)
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "dev command failed: %v\n", err)
		return 1
	}
	return 0
}

func printDevUsage(w io.Writer) {
	fmt.Fprint(w, `Cassini dev commands expose the local harness without making it the product boundary.

Usage:
  cassini dev stack <plan|up|status|down>
  cassini dev room create
  cassini dev smoke
  cassini dev ci-e2e
  cassini dev fixture <prepare-showcase|stream-showcase>
  cassini dev play --room <name> [--nextcloud-host <host-or-url>] [--mode single|full] [--duration <seconds>]
  cassini dev play-private --scaffold-only [--nextcloud-host <host-or-url>]
  cassini dev player <video|showcase|three-songs>
`+"\n")
}
