package operator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// fileSHA256 is the digest that binds the whole delivery chain together
// (D-583): the seal stage records it for the artifact it just packed, the
// publish worker re-checks the sealed file before it spawns anything, and the
// sink re-checks the copy it staged before committing it. Three independent
// checks over the same number, so "the meeting the user downloads is the
// meeting this attempt sealed" is verified rather than assumed.
//
// This deliberately hashes the complete container bytes. The portable
// manifest separately binds the compressed Opus audio essence, which survives
// metadata-only rewrites. The claim here is narrower: "is this the same sealed
// file?".
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
