package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	configSrc := envOr("CONFIG_SOURCE", "/etc/claw/config")
	configDst := envOr("CONFIG_DEST", "/data/config")
	workspacePath := envOr("WORKSPACE_PATH", "/workspace")

	fmt.Println("claw-init: starting")

	// Ensure destination directories exist.
	for _, dir := range []string{configDst, workspacePath} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create directory %s: %v\n", dir, err)
			os.Exit(1)
		}
		fmt.Printf("claw-init: ensured directory %s\n", dir)
	}

	// Merge config files from source to destination.
	srcFile := filepath.Join(configSrc, "config.json")
	dstFile := filepath.Join(configDst, "config.json")

	if err := mergeConfig(srcFile, dstFile); err != nil {
		fmt.Fprintf(os.Stderr, "claw-init: config merge failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("claw-init: done")
}

func mergeConfig(srcPath, dstPath string) error {
	srcData, err := os.ReadFile(srcPath) //nolint:gosec // G304: path is from trusted env var, not user input
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("claw-init: no source config at %s, writing empty config\n", srcPath)
			return os.WriteFile(dstPath, []byte("{}"), 0o600)
		}
		return fmt.Errorf("failed to read source config: %w", err)
	}

	// Validate JSON.
	var parsed map[string]any
	if err := json.Unmarshal(srcData, &parsed); err != nil {
		return fmt.Errorf("source config is not valid JSON: %w", err)
	}

	// Write pretty-printed config to destination.
	pretty, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(dstPath, pretty, 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("claw-init: wrote config to %s (%d bytes)\n", dstPath, len(pretty))
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
