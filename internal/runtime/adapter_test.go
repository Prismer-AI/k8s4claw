package runtime

import (
	"context"
	"testing"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// probeExpectation defines expected probe configuration.
type probeExpectation struct {
	probeType    string // "http", "exec", or "tcp"
	path         string // for HTTP probes
	port         int32  // for HTTP/TCP probes
	cmd          string // for Exec probes
	initialDelay int32
	period       int32
}

// adapterTestCase defines expected behavior for a runtime adapter.
type adapterTestCase struct {
	name    string
	adapter RuntimeAdapter
	runtime v1alpha1.RuntimeType

	wantPort      int
	wantWorkspace string
	wantEnvKey    string
	wantEnvValue  string
	wantShutdown  int32
	wantHealth    probeExpectation
	wantReady     probeExpectation
}

func allAdapterTests() []adapterTestCase {
	return []adapterTestCase{
		{
			name:    "OpenClaw",
			adapter: &OpenClawAdapter{},
			runtime: v1alpha1.RuntimeOpenClaw,

			wantPort:      18900,
			wantWorkspace: "/workspace",
			wantEnvKey:    "OPENCLAW_MODE",
			wantEnvValue:  "gateway",
			wantShutdown:  30,

			wantHealth: probeExpectation{
				probeType: "http", path: "/health", port: 18900,
				initialDelay: 10, period: 15,
			},
			wantReady: probeExpectation{
				probeType: "http", path: "/ready", port: 18900,
				initialDelay: 5, period: 10,
			},
		},
		{
			name:    "NanoClaw",
			adapter: &NanoClawAdapter{},
			runtime: v1alpha1.RuntimeNanoClaw,

			wantPort:      19000,
			wantWorkspace: "/workspace",
			wantEnvKey:    "NANOCLAW_MODE",
			wantEnvValue:  "container",
			wantShutdown:  15,

			wantHealth: probeExpectation{
				probeType: "tcp", port: 19000,
				initialDelay: 5, period: 10,
			},
			wantReady: probeExpectation{
				probeType: "tcp", port: 19000,
				initialDelay: 3, period: 5,
			},
		},
		{
			name:    "ZeroClaw",
			adapter: &ZeroClawAdapter{},
			runtime: v1alpha1.RuntimeZeroClaw,

			wantPort:      3000,
			wantWorkspace: "/workspace",
			wantEnvKey:    "ZEROCLAW_MODE",
			wantEnvValue:  "gateway",
			wantShutdown:  5,

			wantHealth: probeExpectation{
				probeType: "http", path: "/health", port: 3000,
				initialDelay: 3, period: 10,
			},
			wantReady: probeExpectation{
				probeType: "http", path: "/ready", port: 3000,
				initialDelay: 1, period: 5,
			},
		},
		{
			name:    "PicoClaw",
			adapter: &PicoClawAdapter{},
			runtime: v1alpha1.RuntimePicoClaw,

			wantPort:      8080,
			wantWorkspace: "/workspace",
			wantEnvKey:    "PICOCLAW_MODE",
			wantEnvValue:  "serverless",
			wantShutdown:  2,

			wantHealth: probeExpectation{
				probeType: "tcp", port: 8080,
				initialDelay: 1, period: 5,
			},
			wantReady: probeExpectation{
				probeType: "tcp", port: 8080,
				initialDelay: 1, period: 3,
			},
		},
		{
			name:    "IronClaw",
			adapter: &IronClawAdapter{},
			runtime: v1alpha1.RuntimeIronClaw,

			wantPort:      3001,
			wantWorkspace: "/workspace",
			wantEnvKey:    "IRONCLAW_MODE",
			wantEnvValue:  "gateway",
			wantShutdown:  30,

			wantHealth: probeExpectation{
				probeType: "http", path: "/health", port: 3001,
				initialDelay: 15, period: 15,
			},
			wantReady: probeExpectation{
				probeType: "http", path: "/ready", port: 3001,
				initialDelay: 10, period: 10,
			},
		},
	}
}

func TestAdapter_DefaultConfig(t *testing.T) {
	t.Parallel()
	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.adapter.DefaultConfig()

			if cfg.GatewayPort != tt.wantPort {
				t.Errorf("GatewayPort = %d; want %d", cfg.GatewayPort, tt.wantPort)
			}
			if cfg.WorkspacePath != tt.wantWorkspace {
				t.Errorf("WorkspacePath = %q; want %q", cfg.WorkspacePath, tt.wantWorkspace)
			}
			if cfg.Environment == nil {
				t.Fatal("Environment is nil")
			}
			val, ok := cfg.Environment[tt.wantEnvKey]
			if !ok {
				t.Errorf("Environment missing key %q", tt.wantEnvKey)
			} else if val != tt.wantEnvValue {
				t.Errorf("Environment[%q] = %q; want %q", tt.wantEnvKey, val, tt.wantEnvValue)
			}
		})
	}
}

func TestAdapter_GracefulShutdownSeconds(t *testing.T) {
	t.Parallel()
	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.adapter.GracefulShutdownSeconds()
			if got != tt.wantShutdown {
				t.Errorf("GracefulShutdownSeconds() = %d; want %d", got, tt.wantShutdown)
			}
		})
	}
}

func TestAdapter_HealthProbe(t *testing.T) {
	t.Parallel()
	claw := &v1alpha1.Claw{}

	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			probe := tt.adapter.HealthProbe(claw)
			if probe == nil {
				t.Fatal("HealthProbe() returned nil")
			}
			assertProbe(t, probe, tt.wantHealth)
		})
	}
}

func TestAdapter_ReadinessProbe(t *testing.T) {
	t.Parallel()
	claw := &v1alpha1.Claw{}

	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			probe := tt.adapter.ReadinessProbe(claw)
			if probe == nil {
				t.Fatal("ReadinessProbe() returned nil")
			}
			assertProbe(t, probe, tt.wantReady)
		})
	}
}

func TestAdapter_PodTemplate(t *testing.T) {
	t.Parallel()
	claw := &v1alpha1.Claw{}

	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmpl := tt.adapter.PodTemplate(claw)
			if tmpl == nil {
				t.Fatal("PodTemplate() returned nil")
			}

			// Verify 1 init container named "claw-init".
			if got := len(tmpl.Spec.InitContainers); got != 1 {
				t.Fatalf("InitContainers count = %d; want 1", got)
			}
			if tmpl.Spec.InitContainers[0].Name != "claw-init" {
				t.Errorf("InitContainers[0].Name = %q; want %q", tmpl.Spec.InitContainers[0].Name, "claw-init")
			}

			// Verify 1 container named "runtime".
			if got := len(tmpl.Spec.Containers); got != 1 {
				t.Fatalf("Containers count = %d; want 1", got)
			}
			runtime := tmpl.Spec.Containers[0]
			if runtime.Name != "runtime" {
				t.Errorf("Containers[0].Name = %q; want %q", runtime.Name, "runtime")
			}

			// Verify runtime container has the correct port.
			if len(runtime.Ports) == 0 {
				t.Fatal("runtime container has no ports")
			}
			if got := runtime.Ports[0].ContainerPort; got != int32(tt.wantPort) {
				t.Errorf("runtime port = %d; want %d", got, tt.wantPort)
			}

			// Verify at least 4 volumes (ipc-socket, wal-data, config-vol, tmp).
			if got := len(tmpl.Spec.Volumes); got < 4 {
				t.Errorf("Volumes count = %d; want >= 4", got)
			}

			// Verify LivenessProbe and ReadinessProbe are set.
			if runtime.LivenessProbe == nil {
				t.Error("runtime LivenessProbe is nil")
			}
			if runtime.ReadinessProbe == nil {
				t.Error("runtime ReadinessProbe is nil")
			}
		})
	}
}

func TestAdapter_Validate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	spec := &v1alpha1.ClawSpec{}

	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := tt.adapter.Validate(ctx, spec)
			if len(errs) != 0 {
				t.Errorf("Validate() returned %d errors; want 0", len(errs))
			}
		})
	}
}

func TestAdapter_ValidateUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	oldSpec := &v1alpha1.ClawSpec{}
	newSpec := &v1alpha1.ClawSpec{}

	for _, tt := range allAdapterTests() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			errs := tt.adapter.ValidateUpdate(ctx, oldSpec, newSpec)
			if len(errs) != 0 {
				t.Errorf("ValidateUpdate() returned %d errors; want 0", len(errs))
			}
		})
	}
}

// TestAdapter_ShutdownOrdering verifies shutdown seconds follow expected ordering:
// PicoClaw < ZeroClaw < NanoClaw < OpenClaw == IronClaw
func TestAdapter_ShutdownOrdering(t *testing.T) {
	t.Parallel()
	pico := (&PicoClawAdapter{}).GracefulShutdownSeconds()
	zero := (&ZeroClawAdapter{}).GracefulShutdownSeconds()
	nano := (&NanoClawAdapter{}).GracefulShutdownSeconds()
	open := (&OpenClawAdapter{}).GracefulShutdownSeconds()
	iron := (&IronClawAdapter{}).GracefulShutdownSeconds()

	if !(pico < zero && zero < nano && nano < open) {
		t.Errorf("shutdown ordering violated: pico=%d, zero=%d, nano=%d, open=%d; want pico < zero < nano < open",
			pico, zero, nano, open)
	}
	if !(nano < iron) {
		t.Errorf("shutdown ordering violated: nano=%d, iron=%d; want nano < iron", nano, iron)
	}
}

// TestAdapter_UniqueGatewayPorts verifies all runtimes use distinct gateway ports.
func TestAdapter_UniqueGatewayPorts(t *testing.T) {
	t.Parallel()
	seen := make(map[int]string)
	for _, tt := range allAdapterTests() {
		cfg := tt.adapter.DefaultConfig()
		if other, ok := seen[cfg.GatewayPort]; ok {
			t.Errorf("%s and %s share gateway port %d", tt.name, other, cfg.GatewayPort)
		}
		seen[cfg.GatewayPort] = tt.name
	}
}

// TestAdapter_RegistryRoundTrip verifies all adapters can be registered and retrieved.
func TestAdapter_RegistryRoundTrip(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	for _, tt := range allAdapterTests() {
		r.Register(tt.runtime, tt.adapter)
	}
	for _, tt := range allAdapterTests() {
		got, ok := r.Get(tt.runtime)
		if !ok {
			t.Errorf("Get(%q) returned false after registration", tt.runtime)
			continue
		}
		if got != tt.adapter {
			t.Errorf("Get(%q) returned wrong adapter instance", tt.runtime)
		}
	}
}

// assertProbe validates a probe's configuration.
func assertProbe(t *testing.T, probe *corev1.Probe, want probeExpectation) {
	t.Helper()

	switch want.probeType {
	case "http":
		if probe.HTTPGet == nil {
			t.Fatal("expected HTTPGet probe; got nil")
		}
		if probe.HTTPGet.Path != want.path {
			t.Errorf("HTTPGet.Path = %q; want %q", probe.HTTPGet.Path, want.path)
		}
		if probe.HTTPGet.Port.IntValue() != int(want.port) {
			t.Errorf("HTTPGet.Port = %d; want %d", probe.HTTPGet.Port.IntValue(), want.port)
		}
	case "exec":
		if probe.Exec == nil {
			t.Fatal("expected Exec probe; got nil")
		}
		if len(probe.Exec.Command) < 3 {
			t.Fatalf("Exec.Command has %d elements; want at least 3", len(probe.Exec.Command))
		}
		if probe.Exec.Command[2] != want.cmd {
			t.Errorf("Exec.Command[2] = %q; want %q", probe.Exec.Command[2], want.cmd)
		}
	case "tcp":
		if probe.TCPSocket == nil {
			t.Fatal("expected TCPSocket probe; got nil")
		}
		if probe.TCPSocket.Port.IntValue() != int(want.port) {
			t.Errorf("TCPSocket.Port = %d; want %d", probe.TCPSocket.Port.IntValue(), want.port)
		}
	default:
		t.Fatalf("unknown probe type %q", want.probeType)
	}

	if probe.InitialDelaySeconds != want.initialDelay {
		t.Errorf("InitialDelaySeconds = %d; want %d", probe.InitialDelaySeconds, want.initialDelay)
	}
	if probe.PeriodSeconds != want.period {
		t.Errorf("PeriodSeconds = %d; want %d", probe.PeriodSeconds, want.period)
	}
}
