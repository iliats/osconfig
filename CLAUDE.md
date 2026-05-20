# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project overview

Google OS Config Agent — a Go daemon that runs on Google Compute Engine VMs (Linux and Windows) and implements three Cloud OS Config features:

- **OS inventory management** — collects installed packages, OS info, Windows updates and reports to the OS Config API.
- **OS patch management** — applies patch jobs (apt / yum / zypper / googet / WUA) on demand.
- **OS policies / guest policies** — declarative resource state (packages, repositories, files, exec) reconciled from policy assignments.

The agent is published as `google-osconfig-agent` and shipped via deb / rpm / googet packages (see [packaging/](packaging/)).

Module path: `github.com/GoogleCloudPlatform/osconfig`. Go version: see [go.mod](go.mod) (currently 1.25.x).

## Repository layout

Top-level entry points:
- [main.go](main.go) — cross-platform entry, flag parsing, service loop, subcommand dispatch (`run`, `inventory`, `policies`, `waitfortasknotification`, `wuaupdates`).
- [main_linux.go](main_linux.go), [main_windows.go](main_windows.go) — OS-specific service plumbing (systemd vs. Windows service).

Core packages:
- [agentconfig/](agentconfig/) — reads metadata-server config (`WatchConfig`), exposes typed accessors (`OSInventoryEnabled()`, `Debug()`, `SvcEndpoint()`, repo paths, etc.). Single source of truth for runtime config.
- [agentendpoint/](agentendpoint/) — gRPC client for the OS Config agent endpoint API. Handles `RegisterAgent`, `WaitForTaskNotification`, `ReportInventory`, and the three task types (`config_task.go`, `exec_task.go`, `patch_task.go`). `inventory.go` builds inventory payloads.
- [config/](config/) — OS policy resource implementations: `package_resource`, `repository_resource`, `file_resource`, `exec_resource`. Each resource has a `Validate / CheckState / EnforceState` lifecycle.
- [policies/](policies/) — legacy guest policies (repo + package management for apt/yum/zypper/googet, plus software recipes under `recipes/`).
- [ospatch/](ospatch/) — patch execution per package manager (`apt_upgrade.go`, `yum_update.go`, `zypper_patch.go`, `googet_update.go`) and reboot logic.
- [packages/](packages/) — package-manager abstractions (apt, yum, zypper, googet, gem, pip, rpm, msi, WUA, QFE, Windows applications, COS). Files use `_linux` / `_windows` / `_stub` build tag suffixes.
- [inventory/](inventory/) — assembles the inventory snapshot from `osinfo` + `packages`.
- [osinfo/](osinfo/) — OS detection (distro, version, kernel, architecture).
- [clog/](clog/) — context-aware structured logger wrapping `guest-logging-go`.
- [tasker/](tasker/) — single-worker task queue. All long-running work funnels through `tasker.Enqueue` so only one task runs at a time.
- [retryutil/](retryutil/), [util/](util/), [pretty/](pretty/), [attributes/](attributes/), [external/](external/) — small shared helpers.

Other:
- [e2e_tests/](e2e_tests/) — **separate Go module**. Runs the agent on real GCE VMs via Cloud Build ([e2e_tests/cloudbuild.yaml](e2e_tests/cloudbuild.yaml)). Don't include these in unit-test runs.
- [packaging/](packaging/) — deb / rpm / googet spec files.
- [packagebuild/](packagebuild/), [presubmit_packagebuild/](presubmit_packagebuild/) — package build tooling.
- [examples/](examples/) — sample OS policy assignments.
- [.github/workflows/codeql.yml](.github/workflows/codeql.yml) — only CI configured in this repo; tests run elsewhere (Cloud Build / internal infra).

## Common commands

```bash
# Build (host platform)
go build ./...

# Unit tests — single package
go test ./agentconfig/...
go test -run TestWatchConfig ./agentconfig/

# Unit tests — everything except e2e (which is a separate module)
go test ./...

# Race detector
go test -race ./...

# Vet / formatting
go vet ./...
gofmt -l .

# Regenerate gomock mocks (mocks live under util/mocks/)
# Mocks are generated with mockgen — see existing files for the directive.

# Cross-compile for Linux from macOS
GOOS=linux GOARCH=amd64 go build -o /tmp/osconfig_agent .
```

Note: `go.sum` and `go.mod` use a Go toolchain that may be newer than what's installed locally — if `go test` errors out, check `go version` against [go.mod](go.mod).

## Architecture notes

### Configuration flow

All runtime configuration lives in [agentconfig/agentconfig.go](agentconfig/agentconfig.go) and is sourced from the GCE metadata server. `WatchConfig(ctx)` blocks on the metadata server's hanging-GET (`wait_for_change=true`) and updates a global `config` struct on change. Other packages read via accessor functions — **never** read metadata directly from feature code.

Feature toggles (`OSInventoryEnabled`, `GuestPoliciesEnabled`, `TaskNotificationEnabled`) are checked on every iteration of the service loop, so flipping a metadata key takes effect without restart.

### Service loop ([main.go](main.go))

1. `runServiceLoop` runs three goroutines: `runInternalPeriodics` (restart-marker watcher), `runTaskLoop` (manages the `WaitForTaskNotification` streaming RPC), and the main inventory/policies ticker.
2. `WaitForTaskNotification` is a long-lived bidirectional stream from the agent endpoint API. When the server sends a task notification, the agent runs the corresponding task ([agentendpoint/config_task.go](agentendpoint/config_task.go), [exec_task.go](agentendpoint/exec_task.go), [patch_task.go](agentendpoint/patch_task.go)) and reports state back.
3. Restart is signalled by the existence of [agentconfig.RestartFile()](agentconfig/agentconfig.go); the agent exits with code 2 and the supervisor (systemd / Windows SCM) restarts it.

### Cross-platform code

This codebase is heavily cross-platform. Conventions:
- File suffixes: `*_linux.go`, `*_windows.go` use Go build constraints implicitly. `*_stub.go` / `*_stub_linux.go` provide no-op or non-OS implementations to keep the package building on the other OS.
- Don't add `//go:build` tags by hand when the filename suffix already implies the constraint.
- Windows-only deps: `github.com/StackExchange/wmi`, `github.com/go-ole/go-ole`, WUA COM APIs — keep these isolated under `*_windows.go` files.
- If you add a new exported function in a `_linux.go` file, add a stub in a matching `*_windows.go` (or `_stub_windows.go`) so the package compiles on Windows.

### Tasker discipline

`tasker.Enqueue(ctx, name, fn)` serializes work. Don't spawn goroutines that do long-running OS work outside the tasker — concurrent patch / inventory runs will corrupt state.

### Logging

Use [clog](clog/) (not the bare `logger` package) inside request/task code paths so that instance name, agent version, and task IDs flow through as labels:

```go
ctx = clog.WithLabels(ctx, map[string]string{"task_id": id})
clog.Infof(ctx, "...")
clog.Errorf(ctx, "...")
```

Top-of-process initialization in [main.go](main.go) is the one place that still uses `logger.*` directly.

## Code conventions

- License header (Apache 2.0, "Copyright 20XX Google Inc.") at the top of every `.go` file. Match the style of existing files.
- Package comments on the file that names the package: `// Package foo does X.`
- Tests use `github.com/google/go-cmp/cmp`; gomock for interfaces (`github.com/golang/mock/gomock`). Avoid adding `testify` — it isn't a dependency here.
- Error wrapping: `fmt.Errorf("...: %v", err)` is the prevailing style. Don't switch to `%w` without checking the call site uses `errors.Is/As`.
- Public accessors for config (`agentconfig.Foo()`) rather than exported variables. Follow this pattern when adding new metadata-driven config.
- Resource implementations in [config/](config/) follow the `Validate → CheckState → EnforceState` interface from the OS Config proto — mirror existing resources when adding new ones.

## Gotchas

- **e2e_tests is a separate module.** `go test ./...` from the repo root does **not** run them, and dependencies are tracked in [e2e_tests/go.mod](e2e_tests/go.mod). Don't try to unify the modules.
- **Don't read `flag` values outside `main`.** Flags are parsed in `main()`; sub-packages should accept config via function args or `agentconfig` accessors.
- **Inventory first-run jitter** is intentional ([main.go:281](main.go#L281)) — first inventory runs 3–5 minutes after startup to spread load.
- **Windows builds need cgo-free dependencies** for some package manager integrations. If a new dep pulls in cgo, verify Windows still builds.
- **`go.mod` Go version** can be ahead of the toolchain installed locally. If commands fail with toolchain errors, that's the cause.
- **CODEOWNERS / OWNERS** route PRs to the OSConfig team — they will be auto-assigned, no need to ping manually.

## Recommended tooling

All tools below are CLI-invoked (no background daemons). Install once; Claude can call them on demand.

### MCP servers

Configured in [.mcp.json](.mcp.json) at the repo root and auto-enabled via [.claude/settings.json](.claude/settings.json):

- **GitHub MCP** — remote server at `https://api.githubcopilot.com/mcp/`. Needs `GITHUB_PERSONAL_ACCESS_TOKEN` exported in your shell (PAT with `repo` scope). Lets Claude read PRs, issues, and review comments without shelling out for every call.
- **Context7** — launched on demand via `npx -y @upstash/context7-mcp`. Fetches up-to-date docs for the GCP Go SDK (`cloud.google.com/go/osconfig`, `cloud.google.com/go/compute/metadata`), `google.golang.org/api`, and `grpc-go`. Particularly helpful when writing mocks against external interfaces.

Per-developer permission overrides go in `.claude/settings.local.json` (gitignored). The shared allowlist for Go / `gh` commands lives in [.claude/settings.json](.claude/settings.json) and is committed.

### Go toolchain

| Tool | Install | Use it for |
|---|---|---|
| **mockgen** | `go install github.com/golang/mock/mockgen@v1.6.0` | Generate mocks for new interfaces. Existing mocks live under [util/mocks/](util/mocks/) (see [mock_command_runner.go](util/mocks/mock_command_runner.go) header — generated against `util.CommandRunner`). Regenerate with `mockgen -destination util/mocks/mock_<name>.go github.com/GoogleCloudPlatform/osconfig/<pkg> <Interface>`. |
| **gotestsum** | `go install gotest.tools/gotestsum@latest` | Cleaner per-test output, `--rerun-fails`, JUnit XML. Use during iterative test development: `gotestsum --format=testname ./agentconfig/...`. |
| **golangci-lint** | [official install](https://golangci-lint.run/welcome/install/) | Bundles `govet`, `staticcheck`, `errcheck`, `ineffassign`, `unused`, `gosec`. Run on changed packages: `golangci-lint run ./<pkg>/...`. Catches the common test-file mistakes (unused mocks, ignored errors in setup). |
| **govulncheck** | `go install golang.org/x/vuln/cmd/govulncheck@latest` | Security audit against the Go vuln DB. Run before bumping `cloud.google.com/go/*` or `grpc` deps: `govulncheck ./...`. |

### Test writing patterns in this repo

When adding tests, follow the conventions already in the codebase rather than introducing new ones:

- **gomock for interfaces** — define the interface where it's consumed, generate a mock under `util/mocks/` (or a package-local `mocks/` subdir), and drive it from the test:
  ```go
  ctrl := gomock.NewController(t)
  defer ctrl.Finish()
  m := utilmocks.NewMockCommandRunner(ctrl)
  m.EXPECT().Run(gomock.Any(), gomock.Any()).Return([]byte("..."), nil, nil)
  ```
- **HTTP test servers for metadata** — when testing anything that reads from the GCE metadata server, set up an `httptest.NewServer` and point the agent at it via `GCE_METADATA_HOST`. See [agentconfig/agentconfig_test.go:40](agentconfig/agentconfig_test.go#L40) (`setupMockMetadataServer`) for the canonical pattern.
- **Diffs with go-cmp** — never `reflect.DeepEqual` for assertions; use `cmp.Diff(want, got, opts...)` and report `t.Errorf("mismatch (-want +got):\n%s", diff)`. Add `cmpopts.IgnoreUnexported(...)` for proto-generated types.
- **Table-driven tests** — name the slice `tests`, the field `name`, and call `t.Run(tt.name, func(t *testing.T) { ... })`. Most test files in [agentendpoint/](agentendpoint/) follow this shape.
- **Cross-platform tests** — if the code under test is in `*_linux.go` / `*_windows.go`, put the test in `*_linux_test.go` / `*_windows_test.go`. Don't gate at runtime with `runtime.GOOS`.
- **Shared test helpers** live in [util/utiltest/](util/utiltest/). Add new helpers there rather than duplicating per-package.

### Skills worth invoking

A few of the skills available to Claude Code map well to this codebase:

- **`/review`** — run before opening a PR. Pair-reviews the diff for the conventions in this file (license header, cross-platform stubs, mock placement, `clog` vs. `logger`).
- **`/security-review`** — high payoff here. The agent runs as root on customer VMs and shells out to package managers; review changes that touch `packages/`, `ospatch/`, or `config/exec_resource.go` with this skill before merging.
- **`/simplify`** — for new tests especially, this catches over-mocked setups and assertion redundancy. Test files in this repo skew minimal; keep that norm.
- **`/fewer-permission-prompts`** — one-time setup to allowlist the common Go / `gh` commands in [.claude/settings.local.json](.claude/settings.local.json), so iteration on tests doesn't pause for permission prompts.
- **`/gen-tests <file>`** — custom skill ([.claude/commands/gen-tests.md](.claude/commands/gen-tests.md)). Runs coverage on the package, identifies uncovered functions and branches in the given file, and writes new tests consistent with this repo's conventions (table-driven, `want`/`wantErr` naming, `utiltest` helpers, error instance comparison). Example: `/gen-tests agentconfig/agentconfig.go`.

## When making changes

1. Match the conventions of the package you're editing (license header, error style, accessor patterns).
2. Add unit tests in the same package (`*_test.go`). For OS-specific code, add a stub on the other OS so the package keeps building.
3. Run `go build ./...`, `go vet ./...`, and `go test ./<changed-package>/...` before reporting done.
4. For config / metadata changes, update [agentconfig/agentconfig.go](agentconfig/agentconfig.go) **and** its test, then expose a typed accessor.
5. Don't edit anything under [e2e_tests/](e2e_tests/) for unit-test-level work — it's a separate codebase with its own review cycle.
