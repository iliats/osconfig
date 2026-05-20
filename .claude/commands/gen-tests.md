# Generate missing unit tests

Given a Go source file, run coverage analysis, identify uncovered lines, and write new tests that are consistent with the existing test style in this repo.

## Steps

**1. Resolve the target file**

The argument is `$ARGUMENTS`. Treat it as a file path relative to the repo root. If no argument is given, use the file currently open in the IDE.

Derive the Go package import path:
- Strip the filename to get the directory, e.g. `agentconfig/agentconfig.go` → `./agentconfig/...`
- The file might be OS-specific (`*_linux.go`, `*_windows.go`). Note it — tests for OS-specific files go in matching `*_linux_test.go` / `*_windows_test.go` files.

**2. Run coverage**

```bash
go test -coverprofile=/tmp/cover_$(basename $ARGUMENTS .go).out -covermode=atomic ./<package>/...
go tool cover -func=/tmp/cover_$(basename $ARGUMENTS .go).out
```

Parse the output to find functions in the target file that have less than 100% coverage or are entirely uncovered. Ignore functions that are trivially untestable (e.g. `main`, `init`, platform service plumbing like `runService`).

**3. Read source and existing tests**

Read the source file and the corresponding `*_test.go` file (if it exists). Identify:
- What test helpers already exist (especially anything from `util/utiltest/utiltest.go`).
- Which mock types are already imported (from `util/mocks/`).
- Which error sentinel values the package exports or uses internally.
- What the existing test case name style is.

**4. Generate tests**

Write new test functions (or add cases to existing table-driven tests) following these rules:

### Conventions (non-negotiable)

- **License header** — copy the Apache 2.0 header from the source file, updating the year if needed.
- **Package** — use the same package as the existing `*_test.go` (usually the same package name, e.g. `package agentconfig`).
- **Table-driven tests** — use a `tests` slice with a `name string` field. Call `t.Run(tt.name, ...)` for each case.
- **Test case names** — format: `"<what is set up / input>, <what is expected>"`. Examples:
  - `"metadata returns empty endpoint, falls back to prod endpoint"`
  - `"feature flag disabled, returns false"`
  - `"command exits non-zero, returns error"`
- **Result/error field names** — use `want` prefix: `want`, `wantErr`, `wantPath`, `wantOutput`. Never `expected`.
- **Assertions** — prefer the helpers from `util/utiltest`:
  - `utiltest.AssertEquals(t, got, want)` for value equality
  - `utiltest.AssertErrorMatch(t, gotErr, tt.wantErr)` for error equality — this checks both type and message via `reflect.TypeOf` and `.Error()`. Do **not** compare `err.Error()` strings directly.
  - `utiltest.AssertFileContents(t, path, want)` when checking file output
  - `utiltest.AssertFilePath(t, gotPath, tt.wantPath)` when checking file paths
  - `utiltest.MatchSnapshot(t, got, snapshotPath)` for complex structs that are stable across runs
  - `utiltest.OverrideVariable(t, &pkgVar, value)` to temporarily swap package-level variables (automatically restored via `t.Cleanup`)
  - `utiltest.SetExpectedCommands(ctx, mockRunner, commands)` when the code under test shells out via `util.CommandRunner`
  - Fall back to `cmp.Diff(want, got, opts...)` + `t.Errorf("mismatch (-want +got):\n%s", diff)` for proto types or when `AssertEquals` is insufficient; add `protocmp.Transform()` for proto messages and `cmpopts.IgnoreUnexported(...)` as needed.
- **Error instances, not strings** — use `errors.New("exact message")` or the package's sentinel `var` for `wantErr`. Never `wantErr: "some text"`.
- **Metadata server mocking** — for anything that calls `agentconfig` or hits the metadata server, use `setupMockMetadataServer(t, handler)` (defined in `agentconfig/agentconfig_test.go`; copy the pattern into the target package's test file if it's not there yet).
- **Gomock** — for interfaces, set up `ctrl := gomock.NewController(t)` + `defer ctrl.Finish()`, then drive expectations via `.EXPECT()`. Check `util/mocks/` for existing mocks before generating a new one. If a new mock is needed, note the `mockgen` command required but do not run it automatically.
- **Cross-platform** — if the source file is `*_linux.go`, write tests in `*_linux_test.go`. Do not use `runtime.GOOS` guards inside test functions.

### What to cover

Focus on:
1. Functions with 0% line coverage — write at least a happy path and one error/edge case.
2. Branches within partially-covered functions — add table rows that exercise the uncovered branch.
3. Skip: unexported helpers that are only reachable through the tested public surface, OS-service scaffolding, `main`, and any function explicitly marked `// untestable:`.

### What NOT to do

- Do not invent behaviour — read the source and test only what the code actually does.
- Do not rewrite existing passing tests.
- Do not add `testify` — it is not a dependency in this module.
- Do not add a new mock file; note it as a follow-up with the exact `mockgen` command.
- Do not add comments explaining *what* the test does — the test case name already does that.

**5. Output**

Print the complete, ready-to-paste test code (or the full updated `*_test.go` file if it is short). If a new mock is needed, print the `mockgen` command at the end as a separate code block.
