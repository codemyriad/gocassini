---
name: go-coding-standard
description: Repository-specific Go coding standards for Cassini. Use when writing, editing, or reviewing Go code so changes match the repo's current conventions.
---

# Go Coding Standard

This skill is the canonical Go standard for this repo.

It is intentionally conservative: v1 writes down the conventions that already dominate the codebase.

Applies to Go work in:

- `cassini-go-recorder/`
- `cassini-operator/`
- `harness/go-talk-rotator/`

## How to use this skill

- Use it when writing, editing, or reviewing Go code.
- Follow the numbered standards below.
- Prefer surrounding local package patterns when they do not conflict with a numbered standard.
- If you intentionally change the standard, update this file in the same change.
- Keep numbering stable so later edits can refer to specific standards.

## Standards

### 1. Format every touched Go file with `gofmt`.

- Run `gofmt -w` on every touched Go file.
- Do not introduce `gofumpt`, `golangci-lint`, or a new repo-wide lint config just to satisfy v1 style.

### 2. Validate the narrowest sensible repo-root-relative Go test scope.

- After Go changes, run `gofmt`, then run the smallest `go test` scope that exercises the change.
- Broaden validation when you touch shared APIs, cross-package flows, or public behavior.
- Choose validation scope per affected module; this repo is not a single Go module.

Examples:

```bash
cd cassini-operator && go test ./internal/operator
cd cassini-operator && go test ./...
cd cassini-go-recorder && go test ./internal/cassini/...
cd cassini-go-recorder && go test ./...
cd harness/go-talk-rotator && go test ./...
```

### 3. Use the standard `testing` package.

- Prefer the built-in `testing` package.
- Do not add `testify`, `assert`, or `require` as part of this standard.

### 4. Call `t.Helper()` in test helpers that accept `*testing.T`.

- If a helper accepts `*testing.T` and participates in setup, assertions, or failure reporting, call `t.Helper()`.

Example:

```go
func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
```

### 5. Use table-driven tests for real case matrices; keep one-off scenario tests flat.

- Use table-driven tests plus `t.Run(...)` when behavior varies across multiple meaningful cases.
- Keep a test flat when the scenario itself is the clearest way to explain the behavior.

Example:

```go
func TestParseCallURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "valid", raw: "https://cloud.example.com/call/abc123"},
		{name: "missing token", raw: "https://cloud.example.com/call/", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseCallURL(tc.raw)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}
```

### 6. Use `t.Parallel()` only for clearly isolated tests.

- `t.Parallel()` is opt-in.
- Only use it when the test is clearly isolated from env vars, shared ports, shared paths, subprocess state, filesystem collisions, and timing-sensitive interactions.

### 7. Put `context.Context` first on cancelable, I/O-heavy, request-scoped, or long-running operations.

- If a function participates in a request or task lifecycle, performs network I/O, starts subprocess work, blocks for a meaningful time, or produces heavyweight artifacts, accept `context.Context` as the first parameter.
- Do not thread `context.Context` through small pure helpers just for ceremony.

Examples:

```go
func (c *Client) Connect(ctx context.Context) error
func BuildMeetingArtifact(ctx context.Context, mkvPath, outputDir string, cfg BuildConfig, stdout io.Writer) error
```

### 8. Wrap errors with `%w`, inspect with `errors.Is` / `errors.As`, and keep error text lowercase unless a leading token must stay uppercase.

- When adding context to a returned error, use `fmt.Errorf(... %w ...)`.
- For branching and inspection, prefer `errors.Is` and `errors.As` over string matching.
- Keep error text lowercase unless the message must begin with an env var, protocol token, or semantically significant acronym.

Examples:

```go
return fmt.Errorf("open source file %s: %w", sourcePath, err)
```

```go
if errors.Is(err, sql.ErrNoRows) {
	return Job{}, err
}
```

### 9. Prefer concrete types; introduce small consumer-side interfaces only when a seam already exists.

- Default to concrete types.
- Introduce interfaces only when a consumer needs interchangeability or a focused test seam.
- Keep interfaces small.
- Prefer constructors that return concrete types unless returning an interface materially improves the call site.
- Do not invent broad producer-side interfaces "just in case."

Preferred shape:

```go
type LifecycleStore interface {
	Load() (LifecycleState, error)
	Save(LifecycleState) error
}
```

### 10. Avoid `panic` in normal production control flow.

- Return errors for normal runtime, I/O, protocol, orchestration, and validation failures.
- Reserve `panic` for clearly documented impossible invariants, process-fatal bootstrap failures, or tiny helper paths where error propagation would materially worsen the API.

## Review checklist

When reviewing Go code against this standard, check:

1. Was `gofmt` applied to touched Go files?
2. Did validation match the narrowest sensible affected scope?
3. Did tests stay in the standard `testing` style?
4. Do test helpers call `t.Helper()` where appropriate?
5. Are tests table-driven when a real case matrix exists?
6. Is `t.Parallel()` used only where isolation is obvious?
7. Do cancelable or I/O-heavy operations take `context.Context` first?
8. Are errors wrapped and inspected idiomatically, with sensible lowercase messages?
9. Are interfaces justified and small?
10. Is `panic` avoided in normal control flow?
