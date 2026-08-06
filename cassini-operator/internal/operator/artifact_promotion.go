package operator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// promotionBackupSuffix marks the previous destination set aside in the
// staging root while a staged artifact is renamed into place.
const promotionBackupSuffix = ".backup"

func promoteRunBundle(workRoot, sourceRunPath, jobID string) (string, error) {
	destination := canonicalRunPath(workRoot, jobID)
	if err := promoteDirectory(sourceRunPath, destination, currentStagingRoot(workRoot)); err != nil {
		return "", err
	}
	return destination, nil
}

func promoteMeetingBundle(workRoot, sourceMeetingPath, jobID string) (string, error) {
	destination := canonicalMeetingPath(workRoot, jobID)
	if err := promoteDirectory(sourceMeetingPath, destination, currentStagingRoot(workRoot)); err != nil {
		return "", err
	}
	return destination, nil
}

func promoteDirectory(sourcePath, destinationPath, stagingRoot string) error {
	sourcePath = filepath.Clean(sourcePath)
	destinationPath = filepath.Clean(destinationPath)
	stagingRoot = filepath.Clean(stagingRoot)

	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat source artifact: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source artifact is not a directory: %s", sourcePath)
	}
	if err := os.MkdirAll(stagingRoot, 0o755); err != nil {
		return fmt.Errorf("create staging root: %w", err)
	}

	stagingPath := filepath.Join(stagingRoot, filepath.Base(destinationPath))
	_ = os.RemoveAll(stagingPath)
	if err := copyDirectory(sourcePath, stagingPath); err != nil {
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("copy artifact into staging: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("create destination parent: %w", err)
	}
	if err := replaceStagedDirectory(stagingPath, destinationPath, stagingRoot); err != nil {
		return err
	}
	return nil
}

func replaceStagedDirectory(stagingPath, destinationPath, stagingRoot string) error {
	destinationInfo, err := os.Stat(destinationPath)
	hasDestination := err == nil
	if err != nil && !os.IsNotExist(err) {
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("stat destination artifact: %w", err)
	}
	if hasDestination && !destinationInfo.IsDir() {
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("destination artifact is not a directory: %s", destinationPath)
	}

	backupPath := filepath.Join(stagingRoot, filepath.Base(destinationPath)+promotionBackupSuffix)
	if hasDestination {
		_ = os.RemoveAll(backupPath)
		if err := os.Rename(destinationPath, backupPath); err != nil {
			_ = os.RemoveAll(stagingPath)
			return fmt.Errorf("move previous destination artifact aside: %w", err)
		}
	}

	if err := os.Rename(stagingPath, destinationPath); err != nil {
		if hasDestination {
			if rollbackErr := os.Rename(backupPath, destinationPath); rollbackErr != nil {
				return fmt.Errorf("promote staged artifact: %w; rollback previous destination: %v", err, rollbackErr)
			}
		}
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("promote staged artifact: %w", err)
	}

	if hasDestination {
		_ = os.RemoveAll(backupPath)
	}
	return nil
}

// reconcilePromotionStaging repairs leftovers from a promotion interrupted
// between the two renames in replaceStagedDirectory. A "<name>.backup" entry
// whose destination is missing is the previous destination moved aside right
// before the crash: restore it. A backup next to an intact destination means
// the promotion completed: drop it. Anything else in the staging root is an
// orphaned staging copy that promoteDirectory rebuilds from scratch anyway.
// Destinations live in the staging root's parent directory for both layouts
// (<work>/current/.staging -> <work>/current/<name>, <site>.staging -> <site>).
func reconcilePromotionStaging(stagingRoot string) ([]string, error) {
	stagingRoot = filepath.Clean(stagingRoot)
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read promotion staging root: %w", err)
	}
	destinationRoot := filepath.Dir(stagingRoot)
	var actions []string
	for _, entry := range entries {
		name := entry.Name()
		stagedPath := filepath.Join(stagingRoot, name)
		if !strings.HasSuffix(name, promotionBackupSuffix) {
			if err := os.RemoveAll(stagedPath); err != nil {
				return actions, fmt.Errorf("remove orphaned staging copy %s: %w", stagedPath, err)
			}
			actions = append(actions, fmt.Sprintf("removed orphaned staging copy %s", stagedPath))
			continue
		}
		destinationPath := filepath.Join(destinationRoot, strings.TrimSuffix(name, promotionBackupSuffix))
		if _, err := os.Stat(destinationPath); err == nil {
			if err := os.RemoveAll(stagedPath); err != nil {
				return actions, fmt.Errorf("remove stale promotion backup %s: %w", stagedPath, err)
			}
			actions = append(actions, fmt.Sprintf("removed stale promotion backup %s", stagedPath))
			continue
		} else if !os.IsNotExist(err) {
			return actions, fmt.Errorf("stat promotion destination %s: %w", destinationPath, err)
		}
		if err := os.Rename(stagedPath, destinationPath); err != nil {
			return actions, fmt.Errorf("restore promotion backup %s -> %s: %w", stagedPath, destinationPath, err)
		}
		actions = append(actions, fmt.Sprintf("restored %s from promotion backup", destinationPath))
	}
	return actions, nil
}

func copyDirectory(sourceDir, destinationDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read source directory: %w", err)
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(sourceDir, entry.Name())
		destinationPath := filepath.Join(destinationDir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat source entry %s: %w", sourcePath, err)
		}
		if info.IsDir() {
			if err := copyDirectory(sourcePath, destinationPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(sourcePath, destinationPath, info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(sourcePath, destinationPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", sourcePath, err)
	}
	defer source.Close()

	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fmt.Errorf("open destination file %s: %w", destinationPath, err)
	}
	defer destination.Close()

	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("copy file %s -> %s: %w", sourcePath, destinationPath, err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close destination file %s: %w", destinationPath, err)
	}
	return nil
}
