package sdk

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var clawGVR = schema.GroupVersionResource{
	Group:    "claw.prismer.ai",
	Version:  "v1alpha1",
	Resource: "claws",
}

// Client provides a high-level interface for managing Claw agents on Kubernetes.
type Client struct {
	dynamic          dynamic.Interface
	defaultNamespace string
}

// NewClient creates a new k8s4claw SDK client.
func NewClient(opts ...Option) (*Client, error) {
	cfg := &clientConfig{
		defaultNamespace: "default",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	restCfg, err := buildRESTConfig(cfg.kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Client{
		dynamic:          dynClient,
		defaultNamespace: cfg.defaultNamespace,
	}, nil
}

// newClientFromDynamic creates a Client from an existing dynamic.Interface (for testing).
func newClientFromDynamic(dyn dynamic.Interface, ns string) *Client {
	return &Client{dynamic: dyn, defaultNamespace: ns}
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// Try in-cluster first, fall back to default kubeconfig.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, &clientcmd.ConfigOverrides{},
		).ClientConfig()
	}
	return cfg, nil
}

func (c *Client) namespace(ns string) string {
	if ns != "" {
		return ns
	}
	return c.defaultNamespace
}

// Create creates a new Claw agent.
func (c *Client) Create(ctx context.Context, spec *ClawSpec) (*ClawInstance, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec must not be nil")
	}
	if spec.Runtime == "" {
		return nil, fmt.Errorf("runtime must be specified")
	}

	spec.Namespace = c.namespace(spec.Namespace)

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("claw-%d", time.Now().UnixMilli())
	}

	obj := toUnstructured(spec)
	created, err := c.dynamic.Resource(clawGVR).Namespace(spec.Namespace).
		Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Claw: %w", err)
	}

	return fromUnstructured(created), nil
}

// Get returns the current state of a Claw agent.
func (c *Client) Get(ctx context.Context, namespace, name string) (*ClawInstance, error) {
	ns := c.namespace(namespace)
	obj, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Claw %s/%s: %w", ns, name, err)
	}
	return fromUnstructured(obj), nil
}

// List returns all Claw instances in a namespace.
func (c *Client) List(ctx context.Context, namespace string, opts *ListOptions) ([]*ClawInstance, error) {
	ns := c.namespace(namespace)

	listOpts := metav1.ListOptions{}
	if opts != nil {
		if opts.LabelSelector != "" {
			listOpts.LabelSelector = opts.LabelSelector
		}
		if opts.Limit > 0 {
			listOpts.Limit = opts.Limit
		}
	}

	list, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		List(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list Claws in %s: %w", ns, err)
	}

	instances := make([]*ClawInstance, 0, len(list.Items))
	for i := range list.Items {
		instances = append(instances, fromUnstructured(&list.Items[i]))
	}
	return instances, nil
}

// Update patches a Claw agent's configuration.
func (c *Client) Update(ctx context.Context, instance *ClawInstance, update *UpdateSpec) (*ClawInstance, error) {
	if instance == nil {
		return nil, fmt.Errorf("instance must not be nil")
	}
	if update == nil {
		return nil, fmt.Errorf("update spec must not be nil")
	}

	ns := c.namespace(instance.Namespace)
	obj, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Get(ctx, instance.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Claw for update: %w", err)
	}

	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}

	if update.Environment != nil {
		env := make(map[string]interface{}, len(update.Environment))
		for k, v := range update.Environment {
			env[k] = v
		}
		config, _, _ := unstructured.NestedMap(obj.Object, "spec", "config")
		if config == nil {
			config = map[string]interface{}{}
		}
		config["environment"] = env
		spec["config"] = config
	}

	if update.Replicas != nil {
		spec["replicas"] = int64(*update.Replicas)
	}

	if err := unstructured.SetNestedField(obj.Object, spec, "spec"); err != nil {
		return nil, fmt.Errorf("failed to set updated spec: %w", err)
	}

	updated, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update Claw: %w", err)
	}

	return fromUnstructured(updated), nil
}

// Delete removes a Claw agent.
func (c *Client) Delete(ctx context.Context, namespace, name string) error {
	ns := c.namespace(namespace)
	if err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete Claw %s/%s: %w", ns, name, err)
	}
	return nil
}

// WaitForReady watches a Claw until its Phase becomes "Running" or the context expires.
func (c *Client) WaitForReady(ctx context.Context, instance *ClawInstance) error {
	if instance == nil {
		return fmt.Errorf("instance must not be nil")
	}

	ns := c.namespace(instance.Namespace)

	// Check current state first.
	current, err := c.Get(ctx, ns, instance.Name)
	if err != nil {
		return err
	}
	if current.Phase == "Running" {
		return nil
	}

	watcher, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", instance.Name),
		})
	if err != nil {
		return fmt.Errorf("failed to watch Claw: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Claw %s/%s to be ready: %w", ns, instance.Name, ctx.Err())
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed for Claw %s/%s", ns, instance.Name)
			}
			if event.Type == watch.Modified || event.Type == watch.Added {
				obj, ok := event.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
				if phase == "Running" {
					return nil
				}
				if phase == "Failed" {
					return fmt.Errorf("Claw %s/%s entered Failed phase", ns, instance.Name)
				}
			}
		}
	}
}
