package cassini

import (
	"fmt"
	"os"
	"path/filepath"
)

func findRepoRoot() (string, error) {
	if override := os.Getenv("CASSINI_REPO_ROOT"); override != "" {
		resolved, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve CASSINI_REPO_ROOT: %w", err)
		}
		if err := validateRepoRoot(resolved); err != nil {
			return "", err
		}
		return resolved, nil
	}

	start, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current working directory: %w", err)
	}
	return findRepoRootFrom(start)
}

func findRepoRootFrom(start string) (string, error) {
	current := start
	for {
		if err := validateRepoRoot(current); err == nil {
			return current, nil
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return "", fmt.Errorf("could not locate repo root from %s", start)
}

func validateRepoRoot(root string) error {
	required := []string{
		filepath.Join(root, "cassini-go-recorder"),
		filepath.Join(root, "cassini-viewer"),
	}
	for _, path := range required {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("repo root is missing %s", path)
		}
	}
	return nil
}
