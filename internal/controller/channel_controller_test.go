package controller

import (
	"fmt"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestClawChannelReconciler_FinalizerAdded(t *testing.T) {
	ns := fmt.Sprintf("test-ch-fin-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel-finalizer",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Wait for the finalizer to be added by the reconciler.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawChannelFinalizer), nil
	})
}

func TestClawChannelReconciler_ReferenceCount(t *testing.T) {
	ns := fmt.Sprintf("test-ch-ref-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel-ref",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Wait for the finalizer to be added first.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawChannelFinalizer), nil
	})

	// Create a Claw that references this channel.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-ref",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: channel.Name,
					Mode: clawv1alpha1.ChannelModeBidirectional,
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Trigger a re-reconcile of the channel by patching an annotation.
	var latest clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      channel.Name,
		Namespace: channel.Namespace,
	}, &latest); err != nil {
		t.Fatalf("failed to get latest channel: %v", err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	if latest.Annotations == nil {
		latest.Annotations = make(map[string]string)
	}
	latest.Annotations["test.prismer.ai/trigger"] = "ref-count"
	if err := k8sClient.Patch(ctx, &latest, patch); err != nil {
		t.Fatalf("failed to patch channel annotation: %v", err)
	}

	// Wait for the channel status.referenceCount >= 1.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			return false, err
		}
		return fetched.Status.ReferenceCount >= 1, nil
	})

	// Verify referencingClaws contains the claw name.
	var fetched clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      channel.Name,
		Namespace: channel.Namespace,
	}, &fetched); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}
	found := false
	for _, name := range fetched.Status.ReferencingClaws {
		if name == claw.Name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected referencingClaws to contain %q, got %v", claw.Name, fetched.Status.ReferencingClaws)
	}
}

func TestClawChannelReconciler_DeletionProtection(t *testing.T) {
	ns := fmt.Sprintf("test-ch-del-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel-del",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Wait for the finalizer to be added.
	nn := types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawChannelFinalizer), nil
	})

	// Create a Claw that references this channel.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-del",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: channel.Name,
					Mode: clawv1alpha1.ChannelModeBidirectional,
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Trigger channel re-reconcile by patching an annotation.
	var latest clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to get latest channel: %v", err)
	}
	patch := client.MergeFrom(latest.DeepCopy())
	if latest.Annotations == nil {
		latest.Annotations = make(map[string]string)
	}
	latest.Annotations["test.prismer.ai/trigger"] = "deletion-protection"
	if err := k8sClient.Patch(ctx, &latest, patch); err != nil {
		t.Fatalf("failed to patch channel annotation: %v", err)
	}

	// Wait for referenceCount >= 1.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			return false, err
		}
		return fetched.Status.ReferenceCount >= 1, nil
	})

	// Delete the channel.
	var toDelete clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, nn, &toDelete); err != nil {
		t.Fatalf("failed to get channel for deletion: %v", err)
	}
	if err := k8sClient.Delete(ctx, &toDelete); err != nil {
		t.Fatalf("failed to delete channel: %v", err)
	}

	// Wait 2 seconds, then verify the channel still exists (finalizer blocks deletion).
	time.Sleep(2 * time.Second)

	var fetched clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
		t.Fatalf("expected channel to still exist (deletion blocked by finalizer), but got error: %v", err)
	}

	// Verify InUse condition is True.
	inUseCond := apimeta.FindStatusCondition(fetched.Status.Conditions, "InUse")
	if inUseCond == nil {
		t.Fatal("expected InUse condition, not found")
	}
	if inUseCond.Status != metav1.ConditionTrue {
		t.Errorf("expected InUse condition status=True, got %s", inUseCond.Status)
	}
	if inUseCond.Reason != "ReferencesExist" {
		t.Errorf("expected InUse reason=ReferencesExist, got %q", inUseCond.Reason)
	}
}
