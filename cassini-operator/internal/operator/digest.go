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
// This deliberately hashes the container bytes, not the decoded audio. The
// portable format already carries Integrity.PCMSHA256 — a hash of decoded PCM
// that survives a remux and is what the meeting id is derived from — and that
// answers a different question: "is this the same recording?". The claim being
// made here is narrower and about bytes: "is this the same file?". It also
// costs a read instead of an ffmpeg decode.
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
