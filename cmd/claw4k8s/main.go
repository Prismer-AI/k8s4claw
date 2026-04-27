package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func main() {
	log.SetLogger(zap.New(zap.UseDevMode(true)))
	logger := log.Log.WithName("claw4k8s")

	logger.Info("companion claw starting")

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	// Build K8s client config: prefer in-cluster, fall back to KUBECONFIG /
	// ~/.kube/config for local testing.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Info("in-cluster config not available, trying kubeconfig", "err", err.Error())
		cfg, err = ctrl.GetConfig()
		if err != nil {
			logger.Error(err, "failed to load any K8s config (no in-cluster, no kubeconfig)")
			os.Exit(1)
		}
		logger.Info("using kubeconfig for K8s client (out-of-cluster mode)")
	}

	if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
		logger.Error(err, "failed to register CRD scheme")
		os.Exit(1)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		logger.Error(err, "failed to create K8s client")
		os.Exit(1)
	}

	// LLM client: use HermesGatewayClient if configured via env, else noop.
	// Env vars:
	//   LLM_GATEWAY_URL  — e.g. http://hermes-gateway.default.svc.cluster.local:8080
	//   LLM_MODEL        — e.g. anthropic/claude-sonnet-4-20250514
	//   LLM_API_KEY      — optional bearer token
	llm := buildLLMClient(logger)

	pipeline := &Pipeline{LLM: llm, MaxRetries: 3}

	watcher := &Watcher{
		Client:    k8sClient,
		Pipeline:  pipeline,
		Namespace: namespace,
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger.Info("starting escalation watcher", "namespace", namespace)
	if err := watcher.Run(ctx); err != nil {
		logger.Error(err, "watcher exited with error")
		cancel()
		return
	}

	logger.Info("companion claw stopped")
}

// noopLLMClient is a placeholder LLM client that returns immediately so the
// pipeline's fallback path runs without burning the retry/backoff budget.
// Used when no LLM gateway is configured (LLM_GATEWAY_URL unset).
type noopLLMClient struct{}

func (n *noopLLMClient) Analyze(_ context.Context, _ string) (string, string, error) {
	// Return empty result with no error so pipeline.analyze immediately
	// reaches its fallback branch (no retries, no delays).
	return "", "", nil
}

// buildLLMClient returns a HermesGatewayClient when CLAW4K8S_LLM_GATEWAY_URL
// is set, or a noopLLMClient otherwise. Env var names are aligned with
// internal/runtime/k8sops.go (CLAW4K8S_LLM_*).
//
// Falling back to noop allows the watcher to run in environments without an
// LLM — escalations transition to AwaitingApproval with a fallback analysis
// from the pipeline (no retry delays).
func buildLLMClient(logger logr.Logger) LLMClient {
	url := firstEnv("CLAW4K8S_LLM_GATEWAY_URL", "LLM_GATEWAY_URL")
	if url == "" {
		logger.Info("LLM gateway URL not set, using noop LLM client (instant fallback mode)")
		return &noopLLMClient{}
	}
	model := firstEnv("CLAW4K8S_LLM_MODEL", "LLM_MODEL")
	if model == "" {
		model = "anthropic/claude-sonnet-4-20250514"
	}
	apiKey := firstEnv("CLAW4K8S_LLM_API_KEY", "LLM_API_KEY")
	logger.Info("configured Hermes gateway LLM client", "url", url, "model", model)
	return &HermesGatewayClient{
		BaseURL:    url,
		Model:      model,
		APIKey:     apiKey,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// firstEnv returns the first non-empty value among the given environment
// variable names. The first name is the canonical key (CLAW4K8S_*); later
// names are accepted for backward compatibility.
func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
