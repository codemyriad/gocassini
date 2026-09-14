package operator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCassini writes a shell script standing in for the CLI. It records its
// arguments and stdin beside itself, then runs body.
func fakeCassini(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cassini")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$0.args\"\ncat > \"$0.stdin\"\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunAnnotateApplyHonoursTheContract(t *testing.T) {
	bin := fakeCassini(t, `printf '{"format":"cassini.annotate.result.v1","revision":3,"operationId":"op_x","resolved":true,"audioOpusSha256":"aa","containerSha256":"bb","added":["mk_1"]}'`)
	two := 2
	res, err := runAnnotateApply(context.Background(), bin, "in.opus", "out.opus", []byte(`{"ops":[]}`), annotateApplyOptions{
		ActorID: "alice", ActorKind: "agent", OperationID: "op_x", ExpectRevision: &two,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Revision != 3 || res.ContainerSHA256 != "bb" || len(res.Added) != 1 || res.Resolved == nil || !*res.Resolved {
		t.Fatalf("result not decoded: %+v", res)
	}
	args, _ := os.ReadFile(bin + ".args")
	want := "annotate\napply\nin.opus\n--ops\n-\n--actor-id\nalice\n--actor-kind\nagent\n--operation-id\nop_x\n--expect-revision\n2\n--out\nout.opus\n--json\n"
	if string(args) != want {
		t.Fatalf("args:\n%s\nwant:\n%s", args, want)
	}
	stdin, _ := os.ReadFile(bin + ".stdin")
	if string(stdin) != `{"ops":[]}` {
		t.Fatalf("ops must arrive on stdin, got %q", stdin)
	}
}

func TestRunAnnotateApplyRefusesWithoutACaller(t *testing.T) {
	if _, err := runAnnotateApply(context.Background(), "cassini", "a", "b", nil, annotateApplyOptions{}); err == nil {
		t.Fatal("apply without an actor id must refuse before running anything")
	}
}

func TestRunAnnotateMapsExitCodes(t *testing.T) {
	bin := fakeCassini(t, `echo "annotations.items[0].target: [5, 5) is empty or reversed" >&2; exit 4`)
	_, err := runAnnotateShow(context.Background(), bin, "x.opus")
	if annotateExitCode(err) != annotateExitInvalid {
		t.Fatalf("want exit %d, got %v", annotateExitInvalid, err)
	}
	if !strings.Contains(err.Error(), "empty or reversed") {
		t.Fatalf("the CLI's reason must survive: %v", err)
	}
	if annotateExitCode(nil) != 0 {
		t.Fatal("nil is not a CLI error")
	}
}

func TestRunAnnotateRejectsAForeignResult(t *testing.T) {
	bin := fakeCassini(t, `printf '{"format":"something.else"}'`)
	if _, err := runAnnotateCarry(context.Background(), bin, "a", "b", "c"); err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("a result in another format must be refused, got %v", err)
	}
}

func TestRunAnnotateNeedsABinary(t *testing.T) {
	if _, err := runAnnotateShow(context.Background(), " ", "x"); err == nil {
		t.Fatal("no binary must be an error, not a panic")
	}
}
