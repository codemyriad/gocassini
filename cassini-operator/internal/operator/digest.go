package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// fileSHA256 hashes the complete container for seal and publish verification.
// Unlike the manifest's audio digest, this includes metadata bytes.
func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s for digest: %w", path, err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("read %s for digest: %w", path, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
