package routing

import (
	"ccp/internal/config"
	"encoding/json"
	"os"
	"path/filepath"
)

type routingState struct {
	Counter int `json:"counter"`
}

func routingStateDir() string {
	return filepath.Join(config.CcpStateDir(), "routing")
}

func routingStatePath(profile string) string {
	return filepath.Join(routingStateDir(), profile+".json")
}

func ensureRoutingStateDir() error {
	return os.MkdirAll(routingStateDir(), 0o700)
}

func readRoutingCounter(profile string) int {
	data, err := os.ReadFile(routingStatePath(profile))
	if err != nil {
		return 0
	}
	if len(data) == 0 {
		return 0
	}
	var s routingState
	if err := json.Unmarshal(data, &s); err != nil {
		return 0
	}
	if s.Counter < 0 {
		return 0
	}
	return s.Counter
}

func nextRoutingIndex(profile string, poolSize int) (int, error) {
	if poolSize <= 0 {
		return 0, nil
	}
	if err := ensureRoutingStateDir(); err != nil {
		return 0, err
	}
	path := routingStatePath(profile)
	// Acquire file lock best-effort on unix (helpers in daemon_unix.go).
	unlock := lockRoutingState(path)
	defer unlock()

	counter := readRoutingCounter(profile)
	idx := counter % poolSize
	if idx < 0 {
		idx = 0
	}
	next := counter + 1
	s := routingState{Counter: next}
	b, err := json.Marshal(s)
	if err != nil {
		return idx, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return idx, nil
	}
	_ = os.Rename(tmp, path)
	return idx, nil
}

func peekRoutingIndex(profile string, poolSize int) int {
	if poolSize <= 0 {
		return 0
	}
	return readRoutingCounter(profile) % poolSize
}

func clearRoutingState(profile string) {
	_ = os.Remove(routingStatePath(profile))
	_ = os.Remove(routingStatePath(profile) + ".lock")
}

func RoutingStateDir() string                { return routingStateDir() }
func RoutingStatePath(profile string) string { return routingStatePath(profile) }
func EnsureRoutingStateDir() error           { return ensureRoutingStateDir() }
func ReadRoutingCounter(profile string) int  { return readRoutingCounter(profile) }
func NextRoutingIndex(profile string, poolSize int) (int, error) {
	return nextRoutingIndex(profile, poolSize)
}
func PeekRoutingIndex(profile string, poolSize int) int { return peekRoutingIndex(profile, poolSize) }
func ClearRoutingState(profile string)                  { clearRoutingState(profile) }
