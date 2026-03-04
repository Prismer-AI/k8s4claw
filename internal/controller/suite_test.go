package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestMain(m *testing.M) {
	log.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic("failed to start envtest: " + err.Error())
	}
	if cfg == nil {
		panic("envtest config is nil")
	}

	// Register CRD scheme.
	if err := clawv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		panic("failed to add clawv1alpha1 to scheme: " + err.Error())
	}

	// Build the runtime registry with all 4 adapters.
	registry := clawruntime.NewRegistry()
	registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
	registry.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})

	// Create the manager.
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		panic("failed to create manager: " + err.Error())
	}

	// Set up the ClawReconciler.
	if err := (&ClawReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Registry: registry,
	}).SetupWithManager(mgr); err != nil {
		panic("failed to set up ClawReconciler: " + err.Error())
	}

	// Start manager in a goroutine.
	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic("failed to start manager: " + err.Error())
		}
	}()

	// Create a client for tests.
	k8sClient = mgr.GetClient()

	// Run tests.
	code := m.Run()

	// Tear down.
	cancel()
	if err := testEnv.Stop(); err != nil {
		panic("failed to stop envtest: " + err.Error())
	}

	os.Exit(code)
}
