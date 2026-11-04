package modelstore

import (
	"os"
	"regexp"
	"testing"
)

// deployment/models/fetch-models.sh restates the dist artifacts of the Parakeet
// Ultra models so an image or CI job can fetch them without the Go binary. Every line
// it carries must be the catalogue's own mapping, or a fetch verifies against
// digests the app would reject.
func TestFetchModelsScriptMatchesCatalogue(t *testing.T) {
	script, err := os.ReadFile("../../../deployment/models/fetch-models.sh")
	if err != nil {
		t.Fatal(err)
	}
	c := Shipped()
	block := regexp.MustCompile(`(?m)^    ([a-z0-9.-]+)\)\n((?:      echo "[^"]+"\n)+)      ;;`)
	line := regexp.MustCompile(`echo "(\S+) (\S+) ([0-9a-f]{64})"`)
	seen := map[string]bool{}
	for _, b := range block.FindAllStringSubmatch(string(script), -1) {
		id := b[1]
		m, err := c.Model(id, "")
		if err != nil {
			t.Fatalf("fetch-models.sh lists %s: %v", id, err)
		}
		files := map[string]File{}
		for _, f := range m.Files {
			files[f.Path] = f
		}
		lines := line.FindAllStringSubmatch(b[2], -1)
		if len(lines) != len(m.Files) {
			t.Fatalf("%s: script lists %d files, catalogue %d", id, len(lines), len(m.Files))
		}
		for _, l := range lines {
			f, ok := files[l[1]]
			if !ok || f.Artifact != l[2] {
				t.Fatalf("%s/%s: script key %s, catalogue %+v", id, l[1], l[2], f)
			}
			a, err := c.Artifact(f.Artifact)
			if err != nil || a.UncompressedSHA256 != l[3] {
				t.Fatalf("%s/%s: script digest %s, catalogue %+v %v", id, l[1], l[3], a, err)
			}
		}
		seen[id] = true
	}
	for _, id := range []string{"parakeet-ultra-0.6b", "parakeet-ultra-0.6b-int8"} {
		if !seen[id] {
			t.Fatalf("fetch-models.sh does not list %s", id)
		}
	}
}
