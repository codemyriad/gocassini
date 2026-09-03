package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The drift gate (D-718, and check 60 of the eval design).
//
// PR #209 mirrors these prompts into skills/ at the repository root, where
// `npx skills add codemyriad/gocassini` installs them: skills/ is the second
// container path the skills CLI walks, so the mirror needs no manifest and no
// install script. That mirror is bytes copied, and the repository already
// carries one pair of copies gated by nothing at all (img/app.svg and the
// operator's embedded copy of it), which is the failure mode this exists to
// stop repeating.
//
// The rule: for every prompt this package embeds, the copy under
// skills/<skill>/prompts/<same name> must be byte-identical. The authoring home
// and the bytes that ship are then provably the same text, and the SHA-256 an
// artifact records describes both.
//
// Where this test runs matters as much as what it asserts. lint.yml is not
// path-filtered and is a required check; ci.yml's paths-ignore skips
// '**/*.md', so a pull request that edits only a prompt — which changes what
// the product sends, through go:embed — runs zero Go tests there. That is why
// lint.yml invokes this package.

// repoRoot locates the repository root from this package's directory.
//
// Four levels up: cassini-go-recorder/internal/insight/workflows. The marker
// check is not paranoia — a moved package would otherwise make this test read
// an unrelated directory, find no mirror, and pass by describing nothing.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "appinfo", "info.xml")); err != nil {
		t.Fatalf("looked for the repository root at %s and did not find appinfo/info.xml; this test locates it by relative path, so moving this package breaks it: %v", root, err)
	}
	return root
}

func TestSkillPromptsAreByteIdenticalToTheShippedOnes(t *testing.T) {
	root := repoRoot(t)
	skillsDir := filepath.Join(root, "skills")

	if _, err := os.Stat(skillsDir); errors.Is(err, fs.ErrNotExist) {
		// Stated out loud rather than passing quietly. The mirror arrives with
		// #209; until it does there is exactly one copy of every prompt in this
		// tree, which is the strongest state this gate can be in and also the
		// one where it has nothing to compare. The hash pins in
		// workflows_test.go are what guard the single copy meanwhile.
		t.Skip("skills/ does not exist in this tree yet (it arrives with #209), so there is no second copy to compare; the hash pins still gate the embedded one")
	} else if err != nil {
		t.Fatalf("stat %s: %v", skillsDir, err)
	}

	for _, item := range shipped {
		for _, name := range []string{item.SystemFile, item.TemplateFile} {
			embedded, err := readPrompt(name)
			if err != nil {
				t.Fatalf("workflow %q: %v", item.ID, err)
			}
			mirror := filepath.Join(skillsDir, item.SkillDir, "prompts", name)
			copied, err := os.ReadFile(mirror)
			if err != nil {
				// A missing mirror is a failure once skills/ exists: a skill
				// that installs without the prompt the product runs is a skill
				// describing a contract nobody can reproduce.
				t.Errorf("workflow %q ships %s but skills/%s/prompts/%s is missing: %v", item.ID, name, item.SkillDir, name, err)
				continue
			}
			if string(copied) != embedded {
				t.Errorf("workflow %q: %s has drifted from its authoring home.\n  embedded sha256=%s (internal/insight/workflows/prompts/%s)\n  skills   sha256=%s (skills/%s/prompts/%s)\nThese must be the same bytes: the product runs the embedded copy and the skill publishes the other.",
					item.ID, name,
					digest(embedded), name,
					digest(string(copied)), item.SkillDir, name)
			}
		}
	}
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
