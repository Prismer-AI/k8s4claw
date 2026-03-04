package controller

import (
	"context"
	"fmt"
	"sort"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ChannelNameIndexField is the field index key used to look up Claws by referenced channel name.
const ChannelNameIndexField = "spec.channels.name"

// SetupChannelNameIndex registers a field indexer on Claw resources that extracts
// all referenced channel names from spec.channels[].name. This enables efficient
// lookups via client.MatchingFields instead of listing all Claws and filtering.
func SetupChannelNameIndex(mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&clawv1alpha1.Claw{},
		ChannelNameIndexField,
		func(obj client.Object) []string {
			claw, ok := obj.(*clawv1alpha1.Claw)
			if !ok {
				return nil
			}
			names := make([]string, 0, len(claw.Spec.Channels))
			for _, ch := range claw.Spec.Channels {
				names = append(names, ch.Name)
			}
			return names
		},
	)
}

// clawsReferencingChannel returns the sorted names of all Claw resources in the
// given namespace whose spec.channels references the specified channel name.
// It uses the field indexer registered by SetupChannelNameIndex.
func clawsReferencingChannel(ctx context.Context, c client.Client, namespace, channelName string) ([]string, error) {
	var clawList clawv1alpha1.ClawList
	if err := c.List(ctx, &clawList,
		client.InNamespace(namespace),
		client.MatchingFields{ChannelNameIndexField: channelName},
	); err != nil {
		return nil, fmt.Errorf("failed to list Claws referencing channel %q: %w", channelName, err)
	}

	names := make([]string, 0, len(clawList.Items))
	for i := range clawList.Items {
		names = append(names, clawList.Items[i].Name)
	}
	sort.Strings(names)
	return names, nil
}
