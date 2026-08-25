package cli

import (
	"ccp/internal/config"
	"ccp/internal/profile"
	"ccp/internal/proxy"
	"ccp/internal/routing"
	"ccp/internal/settings"
	"ccp/internal/tui"
	"ccp/internal/util"
)

// constants
const (
	cReset  = util.CReset
	cBold   = util.CBold
	cDim    = util.CDim
	cRed    = util.CRed
	cGreen  = util.CGreen
	cYellow = util.CYellow
	cCyan   = util.CCyan
)

// util delegates
func paint(code, s string) string        { return util.Paint(code, s) }
func die(format string, a ...any)        { util.Die(format, a...) }
func warnf(format string, a ...any)      { util.Warnf(format, a...) }
func infof(format string, a ...any)      { util.Infof(format, a...) }
func okf(format string, a ...any)        { util.Okf(format, a...) }
func maskSecret(s string) string         { return util.MaskSecret(s) }
func expandEnvVars(s string) string      { return util.ExpandEnvVars(s) }
func homeDir() string                    { return util.HomeDir() }
func fileExists(p string) bool           { return util.FileExists(p) }
func lookPathAll(names ...string) string { return util.LookPathAll(names...) }

// config delegates
func mustLoadConfig() *config.Config { return config.MustLoadConfig() }
func ccpConfigDir() string           { return config.CcpConfigDir() }
func ccpStateDir() string            { return config.CcpStateDir() }
func proxyLogPath() string           { return config.ProxyLogPath() }
func safeName(n string) bool         { return config.SafeName(n) }

// profile delegates (profile.BuiltEnv is profile.BuiltEnv)
func buildEnv(cfg *config.Config, name string, p *config.Profile) (*profile.BuiltEnv, error) {
	return profile.BuildEnv(cfg, name, p)
}
func buildEnvPeek(cfg *config.Config, name string, p *config.Profile) (*profile.BuiltEnv, error) {
	return profile.BuildEnvPeek(cfg, name, p)
}
func assembleEnv(environ []string, strips []string, sets map[string]string) []string {
	return profile.AssembleEnv(environ, strips, sets)
}

// routing delegates
func peekRoutingIndex(profile string, poolSize int) int {
	return routing.PeekRoutingIndex(profile, poolSize)
}
func clearRoutingState(profile string) { routing.ClearRoutingState(profile) }

// proxy delegates
func proxyBaseURL(cfg *config.Config) string                { return proxy.ProxyBaseURL(cfg) }
func proxyReachable(cfg *config.Config) bool                { return proxy.ProxyReachable(cfg) }
func findProxyBinary(cfg *config.Config) string             { return proxy.FindProxyBinary(cfg) }
func startProxy(cfg *config.Config) error                   { return proxy.StartProxy(cfg) }
func stopProxy(cfg *config.Config) error                    { return proxy.StopProxy(cfg) }
func fetchProxyModels(cfg *config.Config) ([]string, error) { return proxy.FetchProxyModels(cfg) }
func pidFromFile() (int, bool)                              { return proxy.PidFromFile() }
func readLastLines(path string, n int) []string             { return proxy.ReadLastLines(path, n) }
func followLogs(n int)                                      { proxy.FollowLogs(n) }
func scaffoldProxyConfig(path string)                       { proxy.ScaffoldProxyConfig(path) }
func readProxyAPIKeys(cfg *config.Config) []string          { return profile.ReadProxyAPIKeys(cfg) }
func syncOpenAICompat(cfg *config.Config, name string, p *config.Profile) error {
	return proxy.SyncOpenAICompat(cfg, name, p)
}
func removeOpenAICompat(cfg *config.Config, name string) error {
	return proxy.RemoveOpenAICompat(cfg, name)
}
func ensureProxyForUpstream(cfg *config.Config, name string, p *config.Profile) error {
	return proxy.EnsureProxyForUpstream(cfg, name, p)
}
func isUpstreamSynced(cfg *config.Config, name string, p *config.Profile) (bool, string) {
	return proxy.IsUpstreamSynced(cfg, name, p)
}

// settings delegates
func readClaudeSettings(path string) (*settings.ClaudeSettings, error) {
	return settings.ReadClaudeSettings(path)
}
func findEnvConflicts(keys []string) []settings.EnvConflict {
	return settings.FindEnvConflicts(keys)
}

// tui delegates
func selectOption(label string, options []string, start int) (int, error) {
	return tui.SelectOption(label, options, start)
}
func promptLine(label, def string) string   { return tui.PromptLine(label, def) }
func confirmYN(label string, def bool) bool { return tui.ConfirmYN(label, def) }

func installProxy() error          { return proxy.InstallProxy() }
func inheritModel() (string, bool) { return profile.InheritModel() }
