package cassini

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var operatorExecFn = func(argv0 string, argv []string, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}

func runOperator(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printOperatorUsage(stdout)
		return 0
	}

	switch args[0] {
	case "help", "-h", "--help":
		printOperatorUsage(stdout)
		return 0
	case "start":
		return runOperatorStart(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown operator command %q\n\n", args[0])
		printOperatorUsage(stderr)
		return 2
	}
}

func runOperatorStart(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printOperatorStartUsage(stdout)
			return 0
		}
	}

	operatorBin, err := resolveOperatorBinaryPath()
	if err != nil {
		fmt.Fprintf(stderr, "resolve operator binary: %v\n", err)
		return 1
	}
	if err := validateExecutable(operatorBin); err != nil {
		fmt.Fprintf(stderr, "operator binary: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "operator -> %s\n", operatorBin)
	argv := append([]string{operatorBin}, args...)
	if err := operatorExecFn(operatorBin, argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "exec operator: %v\n", err)
		return 1
	}
	return 0
}

func printOperatorUsage(w io.Writer) {
	fmt.Fprint(w, `Cassini operator helpers.

Usage:
  cassini operator start [args...]

Commands:
  start   Launch the separate cassini-operator binary
`)
}

func printOperatorStartUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  cassini operator start [args...]

Launch the separate cassini-operator binary and forward args through unchanged.
`)
}

func resolveOperatorBinaryPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("CASSINI_OPERATOR_BIN")); value != "" {
		return filepath.Abs(value)
	}
	repoRoot, err := findCassiniRepoRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(repoRoot, "bin", "cassini-operator"), nil
}

func findCassiniRepoRoot() (string, error) {
	if repoRoot := strings.TrimSpace(os.Getenv("CASSINI_REPO_ROOT")); repoRoot != "" {
		if looksLikeCassiniRepoRoot(repoRoot) {
			return repoRoot, nil
		}
		return "", fmt.Errorf("CASSINI_REPO_ROOT=%q is not a Cassini repo root", repoRoot)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if looksLikeCassiniRepoRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", errors.New("could not locate repo root; set CASSINI_REPO_ROOT or CASSINI_OPERATOR_BIN")
}

func looksLikeCassiniRepoRoot(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "cassini-go-recorder", "go.mod")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "cassini")); err != nil {
		return false
	}
	return true
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}
