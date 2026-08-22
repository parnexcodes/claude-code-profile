//nolint:errcheck,staticcheck,unused
package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNextRoutingIndex_Rotates(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_STATE_HOME", dir)
	for _, tc := range []struct {
		call int
		want int
	}{
		{0, 0}, {1, 1}, {2, 2}, {3, 0}, {4, 1},
	} {
		_ = tc
	}
	// 3 accounts
	for i, want := range []int{0, 1, 2, 0, 1} {
		got, err := NextRoutingIndex("codex", 3)
		if err != nil {
			t.Fatalf("call %d err: %v", i, err)
		}
		if got != want {
			t.Fatalf("call %d got %d want %d", i, got, want)
		}
	}
	// check persisted counter
	data, err := os.ReadFile(filepath.Join(dir, "routing", "codex.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var s routingState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Counter != 5 {
		t.Fatalf("counter = %d want 5", s.Counter)
	}
}

func TestPeekDoesNotAdvance(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_STATE_HOME", dir)
	if _, err := NextRoutingIndex("p", 2); err != nil {
		t.Fatalf("next: %v", err)
	}
	peek1 := PeekRoutingIndex("p", 2)
	peek2 := PeekRoutingIndex("p", 2)
	if peek1 != peek2 {
		t.Fatalf("peek should not advance: %d vs %d", peek1, peek2)
	}
	if peek1 != 1 {
		t.Fatalf("peek = %d want 1", peek1)
	}
	// next should still give 1
	got, _ := NextRoutingIndex("p", 2)
	if got != 1 {
		t.Fatalf("next after peek = %d want 1", got)
	}
}

func TestMissingAndCorruptState_Recovers(t *testing.T) {
	tests := []struct {
		name  string
		setup func(dir string)
	}{
		{name: "missing", setup: func(dir string) {}},
		{name: "empty", setup: func(dir string) {
			os.MkdirAll(filepath.Join(dir, "routing"), 0o700)
			os.WriteFile(filepath.Join(dir, "routing", "x.json"), []byte{}, 0o600)
		}},
		{name: "invalid_json", setup: func(dir string) {
			os.MkdirAll(filepath.Join(dir, "routing"), 0o700)
			os.WriteFile(filepath.Join(dir, "routing", "x.json"), []byte("not json"), 0o600)
		}},
		{name: "negative", setup: func(dir string) {
			os.MkdirAll(filepath.Join(dir, "routing"), 0o700)
			os.WriteFile(filepath.Join(dir, "routing", "x.json"), []byte(`{"counter": -5}`), 0o600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CCP_STATE_HOME", dir)
			tc.setup(dir)
			got, err := NextRoutingIndex("x", 3)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != 0 {
				t.Fatalf("got %d want 0", got)
			}
			// file should now be valid
			data, err := os.ReadFile(filepath.Join(dir, "routing", "x.json"))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var s routingState
			if err := json.Unmarshal(data, &s); err != nil {
				t.Fatalf("unmarshal after: %v", err)
			}
			if s.Counter != 1 {
				t.Fatalf("counter %d want 1", s.Counter)
			}
		})
	}
}

func TestAtomicTmpRename(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_STATE_HOME", dir)
	_, err := NextRoutingIndex("atomic", 2)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// ensure tmp file not left
	tmp := filepath.Join(dir, "routing", "atomic.json.tmp")
	if _, err := os.Stat(tmp); err == nil {
		t.Fatalf("tmp file should not exist")
	}
}

func TestConcurrent_NextRoutingIndex(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCP_STATE_HOME", dir)
	const n = 20
	var wg sync.WaitGroup
	results := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			got, err := NextRoutingIndex("concurrent", 5)
			if err != nil {
				t.Errorf("err: %v", err)
				return
			}
			results[idx] = got
		}(i)
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(dir, "routing", "concurrent.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var s routingState
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Counter != n {
		t.Fatalf("counter %d want %d", s.Counter, n)
	}
	// file should be valid json and not corrupted
	if s.Counter < 0 {
		t.Fatalf("negative counter")
	}
}
