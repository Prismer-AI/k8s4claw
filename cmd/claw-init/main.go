package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "claw-init: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("claw-init", flag.ContinueOnError)
	configSrc := flags.String("config-source", envOr("CONFIG_SOURCE", "/etc/claw-source"), "source config directory")
	configDst := flags.String("config-dest", envOr("CONFIG_DEST", "/etc/claw"), "writable config directory shared with runtime")
	workspacePath := flags.String("workspace", envOr("WORKSPACE_PATH", "/workspace"), "shared workspace directory")
	mode := flags.String("mode", "overwrite", "overwrite, deepmerge or passthrough")
	flags.String("runtime", "", "runtime name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *mode != "overwrite" && *mode != "deepmerge" && *mode != "passthrough" {
		return fmt.Errorf("unsupported config mode %q", *mode)
	}

	fmt.Println("claw-init: starting")

	// Ensure destination directories exist.
	for _, dir := range []string{*configDst, *workspacePath} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
		fmt.Printf("claw-init: ensured directory %s\n", dir)
	}

	// Merge config files from source to destination.
	srcFile := filepath.Join(*configSrc, "config.json")
	dstFile := filepath.Join(*configDst, "config.json")

	if err := mergeConfig(srcFile, dstFile, *mode); err != nil {
		return fmt.Errorf("config merge failed: %w", err)
	}

	fmt.Println("claw-init: done")
	return nil
}

func mergeConfig(srcPath, dstPath, mode string) error {
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
	if mode == "passthrough" {
		return os.WriteFile(dstPath, srcData, 0o600) //nolint:gosec // G703: destination is an operator-configured volume, never a path from the source JSON.
	}
	if mode == "deepmerge" {
		existing, err := os.ReadFile(dstPath) //nolint:gosec // path is an operator-configured volume
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read destination config: %w", err)
		}
		if len(existing) > 0 {
			var base map[string]any
			if err := json.Unmarshal(existing, &base); err != nil {
				return fmt.Errorf("destination config is not valid JSON: %w", err)
			}
			parsed = deepMerge(base, parsed)
		}
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

func deepMerge(base, overlay map[string]any) map[string]any {
	if base == nil {
		base = make(map[string]any)
	}
	for key, value := range overlay {
		if nested, ok := value.(map[string]any); ok {
			previous, _ := base[key].(map[string]any)
			value = deepMerge(previous, nested)
		}
		base[key] = value
	}
	return base
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
