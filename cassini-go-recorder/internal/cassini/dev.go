package cassini

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
		fmt.Fprint(stderr, "usage: cassini dev stack <up|down|status> [args]\n")
		return 2
	}

	harnessBin := filepath.Join("harness", "bin")
	if devHarnessVMEnabled() && (args[0] == "up" || args[0] == "down") {
		harnessBin = filepath.Join("harness", "vm", "bin")
	}

	switch args[0] {
	case "up":
		return runDevScript(ctx, repoRoot, filepath.Join(harnessBin, "up.sh"), args[1:], stdout, stderr)
	case "down":
		return runDevScript(ctx, repoRoot, filepath.Join(harnessBin, "down.sh"), args[1:], stdout, stderr)
	case "status":
		return runDevScript(ctx, repoRoot, filepath.Join("harness", "bin", "status.sh"), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown dev stack command %q\n", args[0])
		return 2
	}
}

func devHarnessVMEnabled() bool {
	switch os.Getenv("CASSINI_HARNESS_VM") {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
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
  cassini dev stack <up|down|status>
  cassini dev room create
  cassini dev smoke
  cassini dev ci-e2e
  cassini dev fixture <prepare-showcase|stream-showcase>
  cassini dev play --room <name> [--nextcloud-host <host-or-url>] [--mode single|full] [--duration <seconds>]
  cassini dev player <video|showcase|three-songs>
`+"\n")
}
