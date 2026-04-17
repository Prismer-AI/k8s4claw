// Universal mock runtime for k8s4claw integration tests.
//
// If invoked as "claw-init", exits immediately (init container mode).
// Otherwise listens on ALL well-known runtime ports simultaneously,
// serving /health and /ready on each.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// All well-known runtime gateway ports.
var knownPorts = []string{"18900", "19000", "3000", "8080", "3001", "8642"}

func main() {
	// Init container mode: just exit 0.
	if filepath.Base(os.Args[0]) == "claw-init" {
		fmt.Println("claw-init (mock): done")
		return
	}

	runtime := envOr("MOCK_RUNTIME", envOr("CLAW_NAME", "mock"))
	slog.Info("mock runtime starting", "runtime", runtime)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ready") })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "k8s4claw mock: %s\n", runtime) })

	// Listen on all known ports simultaneously.
	var wg sync.WaitGroup
	for _, port := range knownPorts {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			srv := &http.Server{Addr: ":" + p, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
			slog.Info("listening", "port", p)
			if err := srv.ListenAndServe(); err != nil {
				slog.Warn("port unavailable", "port", p, "error", err)
			}
		}(port)
	}
	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
