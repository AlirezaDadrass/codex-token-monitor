package monitor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyExpectedDeviceCountIsIgnored(t *testing.T) {
	for _, count := range []int{0, 3, 101, 1000} {
		c := testConfig(t)
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var old map[string]any
		if err := json.Unmarshal(raw, &old); err != nil {
			t.Fatal(err)
		}
		old["expected_machines"] = count
		path := filepath.Join(t.TempDir(), "config.json")
		if err := atomicJSON(path, old); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("legacy fleet size %d blocks loading: %v", count, err)
		}
		encoded, err := json.Marshal(loaded)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "expected_machines") {
			t.Fatal("new configuration preserves a fleet-size setting")
		}
	}
}
