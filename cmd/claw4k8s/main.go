package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

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

	// TODO: replace with real LLM client (Anthropic SDK).
	llm := &noopLLMClient{}

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
		os.Exit(1)
	}

	logger.Info("companion claw stopped")
}

// noopLLMClient is a placeholder LLM client that always returns a fallback.
type noopLLMClient struct{}

func (n *noopLLMClient) Analyze(_ context.Context, _ string) (string, string, error) {
	return "", "", fmt.Errorf("LLM client not configured")
}
