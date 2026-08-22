## Purpose

Establishes an idiomatic Go package layout for `ccp` so each concern has a single owner, import dependencies flow acyclically, and future changes can be validated by `go vet`/`golangci-lint` per-package.

## ADDED Requirements

### Requirement: Package layout and single responsibility
The repository SHALL be organized as `cmd/ccp` (entrypoint) plus `internal/*` packages, each owning exactly one concern from the current flat layout, and SHALL contain no `package main` outside `cmd/ccp`.

#### Scenario: Entrypoint is thin
- **WHEN** a developer runs `go list ./...`
- **THEN** exactly one package reports `main` at `ccp/cmd/ccp` (or `cmd/ccp` relative to module root) and all other packages are `internal/*`

#### Scenario: One responsibility per package
- **WHEN** the proposed mapping is applied (`config` → `internal/config`, `profile` → `internal/profile`, `routing` → `internal/routing`, `proxy`+`proxycmd` → `internal/proxy`, `claudesettings` → `internal/settings`, `launch` + CLI dispatch → `internal/cli`, `interactive`+`completion`+`doctor` → `internal/cli` or focused subpackages, `util` → `internal/util` or co-located, `daemon_*` → `internal/proxy` or `internal/routing` as appropriate)
- **THEN** no package imports another for unrelated concerns and `go vet ./...` reports no import cycle

#### Scenario: Root contains no stray Go files
- **WHEN** the reorganization completes
- **THEN** `*.go` files exist only under `cmd/` and `internal/` (plus `*_test.go` alongside their package), and the repository root contains only `go.mod`, `go.sum`, `Makefile`, `README.md`, and configuration

### Requirement: Backward-compatible build and binary
The public build contract SHALL remain identical: `go build -o ccp ./cmd/ccp` (or `go build ./...`) produces a single static binary named `ccp` whose CLI surface, version flag, and cross-compile flags are unchanged.

#### Scenario: Build from new entrypoint
- **WHEN** a developer runs `go build -o /tmp/ccp ./cmd/ccp` or `make build` (updated to build `./cmd/ccp`)
- **THEN** the resulting binary prints `ccp <version>` for `./ccp version` and `./ccp --help` shows identical usage to the pre-reorg binary

#### Scenario: Cross-compile remains static
- **WHEN** CI runs `CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION#v}" -o ccp ./cmd/ccp`
- **THEN** the build succeeds on Linux/macOS/Windows for `amd64`/`arm64` and the binary runs without cgo

#### Scenario: Makefile delegates to packages
- **WHEN** `make vet`, `make fmt`, or `golangci-lint run ./...` is invoked
- **THEN** they operate over `./...` (all packages) and pass with the project's default linter config (no `golangci` config file, `openspec/config.yaml` has no custom rules)

### Requirement: Import acyclicity and dependency hygiene
Internal packages SHALL form a directed acyclic graph; `internal/util` (or equivalent leaf) SHALL NOT import higher-level packages, and no package SHALL import `cmd/ccp`.

#### Scenario: Acyclic imports
- **WHEN** `go list -f '{{.ImportPath}}: {{.Imports}}' ./...` is run
- **THEN** no cycle is reported and `cmd/ccp` appears as the sole importer of `internal/cli` (or equivalent top-level orchestrator)

#### Scenario: No new runtime dependencies
- **WHEN** `go.mod` is inspected after the reorg
- **THEN** it contains the same required modules (`github.com/BurntSushi/toml`, `golang.org/x/sys`, `golang.org/x/term`, `gopkg.in/yaml.v3`) plus only any test-only additions explicitly approved, and `go mod tidy` is clean

### Requirement: Config and state paths unchanged
The reorganization SHALL preserve all filesystem contracts: config dir resolution (`$CCP_HOME` > `$XDG_CONFIG_HOME/ccp` > `~/.config/ccp`), state dir resolution, proxy pid/log paths, routing state path (`<state>/routing/<profile>.json`), and profile TOML schema.

#### Scenario: Config loading still respects precedence
- **WHEN** `CCP_HOME` is set to a temp dir containing `config.toml` and `profiles/foo.toml`, and `profiles/foo.toml` collides with `[profiles.foo]` inline in `config.toml`
- **THEN** the `profiles/` file wins, identical to `config.go:179` pre-reorg behavior

#### Scenario: State files remain at same locations
- **WHEN** a pooled profile runs `ccp <profile>` twice
- **THEN** the counter is persisted at `<state>/routing/<profile>.json` (atomic tmp+rename, `flock` on Unix) at the path returned by the pre-reorg `routing.go` logic

### Requirement: CLI surface unchanged
All `ccp` commands, flags, and exit codes SHALL remain identical after the move; no flag is renamed, removed, or re-typed.

#### Scenario: Help output stable
- **WHEN** `ccp --help`, `ccp list`, `ccp show <profile>`, `ccp doctor`, `ccp proxy status`, and `ccp completion zsh|bash` are invoked on the new binary
- **THEN** their stdout/stderr output and exit codes match the pre-reorg binary for the same config

#### Scenario: Profile TOML schema stable
- **WHEN** an existing `profiles/glm.toml` from before the reorg is used with the new binary
- **THEN** it loads without modification and `ccp show glm` displays identical environment overrides

### Requirement: Documentation reflects new layout
`AGENTS.md` and `README.md` SHALL be updated to describe the new package layout, build commands, and where to add new code/tests.

#### Scenario: AGENTS.md updated
- **WHEN** a contributor reads `AGENTS.md` after the change
- **THEN** it lists each `internal/*` package with its single responsibility, the `cmd/ccp` entrypoint, and the `go test ./...` workflow instead of the former "one `package main`, no tests" description

