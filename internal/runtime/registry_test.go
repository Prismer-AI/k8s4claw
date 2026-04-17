package runtime

import (
	"testing"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestNewRegistry(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry() returned nil")
	}
	// Verify fresh registry has no adapters via public API.
	_, ok := r.Get(v1alpha1.RuntimeOpenClaw)
	if ok {
		t.Fatal("fresh registry should have no adapters")
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	adapter := &OpenClawAdapter{}

	r.Register(v1alpha1.RuntimeOpenClaw, adapter)

	got, ok := r.Get(v1alpha1.RuntimeOpenClaw)
	if !ok {
		t.Fatal("Get() returned false for registered adapter")
	}
	if got != adapter {
		t.Error("Get() returned different adapter instance")
	}
}

func TestRegistry_GetUnregistered(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	_, ok := r.Get(v1alpha1.RuntimeOpenClaw)
	if ok {
		t.Error("Get() returned true for unregistered adapter")
	}
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	first := &OpenClawAdapter{}
	second := &OpenClawAdapter{}

	r.Register(v1alpha1.RuntimeOpenClaw, first)
	r.Register(v1alpha1.RuntimeOpenClaw, second)

	got, ok := r.Get(v1alpha1.RuntimeOpenClaw)
	if !ok {
		t.Fatal("Get() returned false after re-register")
	}
	if got != second {
		t.Error("Get() should return the latest registered adapter")
	}
}

func TestRegistry_MultipleRuntimes(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	adapters := map[v1alpha1.RuntimeType]RuntimeAdapter{
		v1alpha1.RuntimeOpenClaw:   &OpenClawAdapter{},
		v1alpha1.RuntimeNanoClaw:   &NanoClawAdapter{},
		v1alpha1.RuntimeZeroClaw:   &ZeroClawAdapter{},
		v1alpha1.RuntimePicoClaw:   &PicoClawAdapter{},
		v1alpha1.RuntimeIronClaw:   &IronClawAdapter{},
		v1alpha1.RuntimeHermesClaw: &HermesClawAdapter{},
	}

	for rt, adapter := range adapters {
		r.Register(rt, adapter)
	}

	for rt, want := range adapters {
		got, ok := r.Get(rt)
		if !ok {
			t.Errorf("Get(%q) returned false", rt)
			continue
		}
		if got != want {
			t.Errorf("Get(%q) returned wrong adapter", rt)
		}
	}

	// Custom should not be registered
	_, ok := r.Get(v1alpha1.RuntimeCustom)
	if ok {
		t.Error("Get(custom) should return false when not registered")
	}
}
