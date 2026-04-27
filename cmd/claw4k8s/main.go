package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
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

	// Build in-cluster K8s client.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Error(err, "failed to get in-cluster config (running outside cluster?)")
		fmt.Fprintln(os.Stderr, "claw4k8s: requires in-cluster config; set KUBERNETES_SERVICE_HOST or use a kubeconfig")
		os.Exit(1)
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

// noopLLMClient is a placeholder LLM client that always returns a fallback.
type noopLLMClient struct{}

func (n *noopLLMClient) Analyze(_ context.Context, _ string) (string, string, error) {
	return "", "", fmt.Errorf("LLM client not configured")
}

// buildLLMClient returns a HermesGatewayClient when LLM_GATEWAY_URL is set,
// or a noopLLMClient otherwise. Falling back to noop allows the watcher to
// run in environments without an LLM (escalations transition to
// AwaitingApproval with a fallback analysis from the pipeline).
func buildLLMClient(logger logr.Logger) LLMClient {
	url := os.Getenv("LLM_GATEWAY_URL")
	if url == "" {
		logger.Info("LLM_GATEWAY_URL not set, using noop LLM client (fallback-only mode)")
		return &noopLLMClient{}
	}
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "anthropic/claude-sonnet-4-20250514"
	}
	logger.Info("configured Hermes gateway LLM client", "url", url, "model", model)
	return &HermesGatewayClient{
		BaseURL: url,
		Model:   model,
		APIKey:  os.Getenv("LLM_API_KEY"),
	}
}
