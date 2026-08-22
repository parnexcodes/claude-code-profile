//nolint:errcheck,staticcheck,unused
package profile

import (
	"testing"
)

func TestManagedVars(t *testing.T) {
	if len(ManagedVars) < 20 {
		t.Fatalf("ManagedVars length %d want >=20", len(ManagedVars))
	}
	need := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK"}
	for _, k := range need {
		found := false
		for _, v := range ManagedVars {
			if v == k {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q", k)
		}
	}
	// no duplicates
	seen := map[string]bool{}
	for _, v := range ManagedVars {
		if seen[v] {
			t.Fatalf("duplicate %q", v)
		}
		seen[v] = true
	}
}
