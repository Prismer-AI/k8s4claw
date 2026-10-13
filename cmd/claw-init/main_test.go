package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRunConfigContract(t *testing.T) {
	for _, mode := range []string{"overwrite", "deepmerge", "passthrough"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			source, dest, workspace := filepath.Join(dir, "source"), filepath.Join(dir, "dest"), filepath.Join(dir, "workspace")
			for _, p := range []string{source, dest} {
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(source, "config.json"), []byte(`{"nested":{"new":2},"override":true}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "config.json"), []byte(`{"nested":{"old":1},"override":false}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := run([]string{"--mode", mode, "--runtime", "openclaw", "--workspace", workspace, "--config-source", source, "--config-dest", dest}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dest, "config.json")) //nolint:gosec // Path is confined to t.TempDir.
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"nested": map[string]any{"new": float64(2)}, "override": true}
			if mode == "deepmerge" {
				want["nested"].(map[string]any)["old"] = float64(1)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
				t.Fatal("workspace was not created", err)
			}
		})
	}
}

func TestRunInvalidConfigAndMode(t *testing.T) {
	if err := run([]string{"--mode", "unsupported"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`invalid`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--config-source", dir, "--config-dest", filepath.Join(dir, "out"), "--workspace", filepath.Join(dir, "ws")}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
