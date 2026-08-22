## 1. Scaffolding and Safety Net

- [x] 1.1 Create package skeleton `cmd/ccp` and `internal/{config,profile,routing,proxy,settings,cli,tui,util}` with `doc.go` stubs; verify `go list ./...` shows new packages and `go vet ./...` still passes on the unchanged root (no moves yet)
- [x] 1.2 Add hermetic test helpers in `internal/testutil` (or inline per package): `TempCCPHome(t) (configDir, stateDir string)`, `WithEnv(t, key, val)`, `MustWriteFile(t, path, body, perm)`, and `NO_COLOR=1` setup; document usage for all later tests
- [x] 1.3 Record baseline golden outputs for `ccp --help`, `ccp list`, and `ccp show <profile>` (pooled + single) on the current binary into `internal/cli/testdata/` so post-move output can be diffed
- [x] 1.4 Baseline CI smoke: run `go vet ./...`, `golangci-lint run ./...`, `go build -o /tmp/ccp-baseline . && /tmp/ccp-baseline version` and save stdout for comparison

## 2. Extract `internal/util` (leaf package)

- [x] 2.1 Move `util.go` to `internal/util/util.go` (and color globals `useColor`, `paint`, `die`/`warnf`/`infof`/`okf`, `homeDir`, `expandPath`, `expandEnvVars`, `maskSecret`, `closeQuietly`, `randHex`, `fileExists`/`isExecutable`, `probeURL`, `lookPathAll`, `procCmdline`); change `package main` → `package util`, export needed symbols (`ExpandEnvVars`, `MaskSecret`, `ExpandPath`, `FileExists`, `ProbeURL`, etc.), keep `util.Die` thin
- [x] 2.2 Update imports in remaining root files to `ccp/internal/util`; ensure no import cycle; verify `go build ./...` + `go vet ./...` pass
- [x] 2.3 Add `internal/util/util_test.go` table-driven tests: `TestExpandEnvVars` (braced `${VAR}`, bare `$VAR`, unknown untouched, empty string), `TestMaskSecret` (empty, ≤10 chars, >10 chars), `TestExpandPath` (`~`, `~/a`, absolute, relative), `TestProbeURL` (httptest server), `TestFileExists/IsExecutable` with `t.TempDir`
- [x] 2.4 Run `go test ./internal/util -count=1` and `golangci-lint run ./internal/util/...`; fix any lint

## 3. Extract `internal/settings` and `internal/routing`

- [x] 3.1 Move `claudesettings.go` → `internal/settings/settings.go`; package `settings`; export `ReadClaudeSettings`, `FindEnvConflicts`, `InheritModel` (or keep unexported + expose via testable API); depend only on `internal/util` + stdlib
- [x] 3.2 Add `internal/settings/settings_test.go`: `TestReadClaudeSettings` (missing file → nil, valid JSON, invalid JSON error), `TestFindEnvConflicts` (user/project/local scopes, no collision vs. collision, warn on bad JSON vs. fail), `TestInheritModel` via temp `HOME/.claude/settings.json`
- [x] 3.3 Move `routing.go` + `daemon_unix.go`/`daemon_other.go` lock helpers → `internal/routing/routing.go` (+ `daemon_unix.go`/`daemon_other.go`); export `NextRoutingIndex`, `PeekRoutingIndex`, `ClearRoutingState`, `RoutingStateDir`; depend on `internal/config` for state dir + `internal/util` for file helpers
- [x] 3.4 Add `internal/routing/routing_test.go`: `TestNextRoutingIndex_Rotates`, `TestPeekDoesNotAdvance`, `TestMissingAndCorruptState_Recovers`, `TestNegativeCounter_Recovers`, `TestAtomicTmpRename`, `TestConcurrent_NextRoutingIndex` (WaitGroup, `flock` on unix; skip strict check on `GOOS=windows`); all use `t.Setenv("CCP_STATE_HOME", t.TempDir())`
- [x] 3.5 Verify `go build ./...` + `go vet ./...` + `go test ./internal/settings ./internal/routing -count=1` pass

## 4. Extract `internal/config`

- [x] 4.1 Move `config.go` → `internal/config/config.go`; package `config`; export `Config`, `Profile`, `Account`, `ProxyConfig`, `LoadConfig`, `MustLoadConfig`, `CcpConfigDir`, `CcpStateDir`, `ProxyPidPath`, `SafeName`, `ValidatePool`; ensure `bootstrap` and template consts move with it; imports only `internal/util` + `BurntSushi/toml` + `gopkg.in/yaml.v3`
- [x] 4.2 Update remaining root files to import `internal/config`; replace bare `safeName`, `fileExists`, `expandPath` etc. with `config.SafeName`/`util.FileExists`; verify `go build ./...` + `go vet ./...` pass
- [x] 4.3 Add `internal/config/config_test.go` integration tests (all with `t.Setenv("CCP_HOME", t.TempDir())` + `CCP_STATE_HOME` for routing): `TestLoad_Precedence_ProfilesDirWins`, `TestLoad_InlineVsFile_Collision`, `TestBootstrap_CreatesSeedsAndNotOverwrite`, `TestSafeName`, `TestValidatePool_ValidAndInvalid`, `TestRoutingStrategy_DefaultRoundRobin`, `TestCcpConfigDir_Precedence_CCPSucceeds_XDG`, `TestProxyConfigDefaults`; assert file modes `0600`/`0700`
- [x] 4.4 Add `internal/config/path_test.go` (or extend above): verify `ccpConfigDir`/`ccpStateDir` resolution for `CCP_HOME`, `XDG_CONFIG_HOME`, `HOME` fallbacks with `t.Setenv`
- [x] 4.5 Verify `go test ./internal/config -count=1` + `golangci-lint run ./internal/config/...` pass

## 5. Extract `internal/proxy`

- [x] 5.1 Move `proxy.go` + `proxycmd.go` (+ `daemon_*` detach/kill helpers that belong to proxy lifecycle) → `internal/proxy/` (`proxy.go`, `proxycmd.go`, `install.go` split as needed); export `FindProxyBinary`, `ProxyReachable`, `ProxyBaseURL`, `StartProxy`, `StopProxy`, `FetchProxyModels`, `InstallProxy`, `Tail` helpers; depend on `internal/config`, `internal/util`
- [x] 5.2 Update callers in remaining root (`launch.go` needs `proxy.ProxyReachable`/`proxy.StartProxy`) to import `internal/proxy`; verify `go build ./...` + `go vet ./...` pass
- [x] 5.3 Add `internal/proxy/proxy_test.go`: `TestFindProxyBinary_Precedence` (config binary > PATH > ~/.local/bin > <state>/bin via temp HOME/PATH/CCP_STATE_HOME), `TestProxyReachable` with `httptest.NewServer`, `TestFetchProxyModels` (auth header, 200 vs non-200, sorted ids), `TestPickAsset` (OS/arch scoring), `TestIsBinaryName`; no real proxy required
- [x] 5.4 Add `internal/proxy/install_test.go` for `extractBinary` (tar.gz/zip/raw) using in-memory archives in `t.TempDir`
- [x] 5.5 Verify `go test ./internal/proxy -count=1` + `golangci-lint run ./internal/proxy/...` pass

## 6. Extract `internal/profile`

- [x] 6.1 Move `profile.go` → `internal/profile/profile.go`; package `profile`; export `ManagedVars`, `ManagedVarsList`, `BuildEnv`, `BuildEnvPeek`, `AssembleEnv`, per-`Account` `ResolveAuth` + `Profile.ResolveAuth`; depend on `internal/config`, `internal/routing`, `internal/settings` (for `inheritModel`), `internal/util`
- [x] 6.2 Resolve ownership of `readProxyAPIKeys`/`proxyYAML` (move to `internal/config` or `internal/proxy` and inject via `Config` so `profile` does not import `proxy` cyclically); update signatures to accept `*config.Config`; verify `go vet ./...` shows no cycle
- [x] 6.3 Add `internal/profile/profile_test.go` — `TestManagedVarsStripped` (parent env has stray `ANTHROPIC_*`/`CLAUDE_CODE_*`/`USE_BEDROCK`), `TestResolveAuth_Priority` (auth_token_env > api_key_env > literal > proxy api-keys[0] > none), `TestBuildEnv_SingleVsPooled`, `TestBuildEnv_Expansion` (baseURL/auth_token with `${VAR}`), `TestEffectiveBaseURL_PerAccountOverride`, `TestAssembleEnv_DeterministicAndSorted`, `TestOnlySelectedCredentialPresent` (3-account pool selects exactly one)
- [x] 6.4 Add `internal/profile/managedvars_test.go`: assert `len(ManagedVars) >= 20`, contains `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, and no duplicates; `BuildEnv` strips all of them
- [x] 6.5 Verify `go test ./internal/profile -count=1` + `golangci-lint run ./internal/profile/...` pass

## 7. Extract `internal/cli` + `cmd/ccp` entrypoint

- [x] 7.1 Move `launch.go` → `internal/cli/launch.go`, `doctor.go` → `internal/cli/doctor.go`, `interactive.go` + `completion.go` → `internal/cli` or `internal/tui` + `internal/cli/completion.go`; move `main.go` dispatch logic into `internal/cli` package and leave `cmd/ccp/main.go` as a thin `package main` wrapper (`func main() { cli.Run(os.Args) }`); export `Launch`, `ShowProfile`, `ListProfiles`, `RunDoctor`, `HandleAdd` etc. as needed
- [x] 7.2 Split `internal/tui` if needed (`internal/tui/tui.go` for `SelectOption`, `PromptLine`, `ConfirmYN`, `ColorEnabled`); make `useColor`/`NO_COLOR` testable (inject or export getter)
- [x] 7.3 Update `cmd/ccp/main.go` imports to `internal/cli` + `internal/config`; verify `go build -o /tmp/ccp ./cmd/ccp && /tmp/ccp version` prints version and `go vet ./...` passes
- [x] 7.4 Add `internal/cli/cli_test.go` with golden files: `TestShow_MasksSecrets` (pooled 3-account, literal + env var), `TestShow_PeekMarker`, `TestList_ShowsPoolSize`, `TestList_DefaultMarker`, `TestDoctor_SurfacesPoolErrors` (bad auth/account name), `TestRenderProfileToml` determinism; capture stdout to `bytes.Buffer`, compare to `testdata/*.golden` with `NO_COLOR=1`
- [x] 7.5 Add `internal/cli/doctor_test.go`: fake `HOME`/`CCP_HOME` temp dirs, stub `claude` on PATH (or absence), stub `settings.json` with `env` block, assert `FindEnvConflicts` reporting path
- [x] 7.6 Verify `go test ./internal/cli ./internal/tui -count=1 -run TestShow -count=1` + full `go test ./... -count=1` all green

## 8. Build, CI, and Docs Update

- [x] 8.1 Update `Makefile`: `build: go build -o ccp ./cmd/ccp`, `vet: go vet ./...`, `fmt: gofmt -w cmd internal .` (or `gofmt -w .`); add `test: go test ./... -count=1` and optional `test-race: go test ./... -race`
- [x] 8.2 Update `.github/workflows/ci.yml`: build job runs `go vet ./...` then `go test ./... -count=1` (and `-race` on ubuntu if feasible) then `go build -o ccp_test ./cmd/ccp && ./ccp_test version`; lint job stays `golangci-lint run ./...` with default config (no config file needed)
- [x] 8.3 Update `.github/workflows/release.yml:37` cross-compile to `go build -trimpath -ldflags "-s -w -X main.version=${VERSION#v}" -o "bin/ccp_${VERSION}_${os}_${arch}${ext}" ./cmd/ccp`
- [x] 8.4 Update `AGENTS.md` and `README.md` with new tree (`cmd/ccp` + `internal/*`), per-package responsibilities, and `go test ./...` instructions; replace "One `package main`, no tests" section
- [x] 8.5 Verify `golangci-lint run ./...`, `go vet ./...`, `gofmt -l .` (empty), `go test ./... -count=1` all pass on clean checkout

## 9. End-to-End Verification and No-Regression Checks

- [x] 9.1 Cross-check against `specs/multi-account-routing/spec.md` — map each scenario (single vs pooled, mixed auth types, round-robin order, state persists, one session keeps one credential, base URL override, only selected credential present, `show`/`list` pool indicator, `doctor` fails on bad pool) to an existing test; add missing cases
- [x] 9.2 Smoke test matrix: `CCP_HOME=$(mktemp -d) go run ./cmd/ccp doctor` (no claude on PATH case), `CCP_HOME=$(mktemp -d) go run ./cmd/ccp list`, `ccp show glm/kimi/official` golden diff vs baseline, `go build -o /tmp/ccp ./cmd/ccp && /tmp/ccp --help`
- [x] 9.3 Run `go test ./... -count=1 -race` on Linux (if race enabled) and `go test ./... -count=1` on macOS/Windows matrix; fix any race or lock regression
- [x] 9.4 Final lint/format gate: `go vet ./...`, `golangci-lint run ./...`, `gofmt -w . && git diff --exit-code` all pass; tag the change as ready for review

