package operator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func promoteRunBundle(workRoot, sourceRunPath, jobID string) (string, error) {
	destination := canonicalRunPath(workRoot, jobID)
	if err := promoteDirectory(sourceRunPath, destination, currentStagingRoot(workRoot), nil); err != nil {
		return "", err
	}
	return destination, nil
}

func promoteMeetingBundle(workRoot, sourceMeetingPath, jobID string) (string, error) {
	destination := canonicalMeetingPath(workRoot, jobID)
	if err := promoteDirectory(sourceMeetingPath, destination, currentStagingRoot(workRoot), nil); err != nil {
		return "", err
	}
	return destination, nil
}

func promoteSiteBundle(sourceSitePath, destinationSitePath string, lineage SiteBundleLineage) error {
	return promoteDirectory(sourceSitePath, destinationSitePath, siteStagingRoot(destinationSitePath), func(stagingPath string) error {
		return WriteSiteBundleLineage(stagingPath, lineage)
	})
}

func promoteDirectory(sourcePath, destinationPath, stagingRoot string, prepareStaged func(string) error) error {
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
	if prepareStaged != nil {
		if err := prepareStaged(stagingPath); err != nil {
			_ = os.RemoveAll(stagingPath)
			return fmt.Errorf("prepare staged artifact: %w", err)
		}
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

	backupPath := filepath.Join(stagingRoot, filepath.Base(destinationPath)+".backup")
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
	return nil
}
