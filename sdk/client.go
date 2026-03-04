package sdk

import (
	"context"
	"fmt"
)

// Client provides a high-level interface for managing Claw agents on Kubernetes.
type Client struct {
	// TODO: add k8s client, config
}

// NewClient creates a new k8s4claw SDK client.
// It uses the default kubeconfig or in-cluster config.
func NewClient() (*Client, error) {
	// TODO: initialize kubernetes client from kubeconfig/in-cluster
	return &Client{}, nil
}

// Create creates a new Claw agent and waits for it to be ready.
func (c *Client) Create(ctx context.Context, spec *ClawSpec) (*ClawInstance, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec must not be nil")
	}
	if spec.Runtime == "" {
		return nil, fmt.Errorf("runtime must be specified")
	}
	if spec.Namespace == "" {
		spec.Namespace = "default"
	}

	// TODO: create Claw CR via kubernetes API
	// TODO: wait for Phase=Running

	return &ClawInstance{
		Name:      "", // TODO: generated name
		Namespace: spec.Namespace,
		Phase:     "Pending",
	}, nil
}

// Get returns the current state of a Claw agent.
func (c *Client) Get(ctx context.Context, namespace, name string) (*ClawInstance, error) {
	// TODO: get Claw CR via kubernetes API
	return nil, fmt.Errorf("not implemented")
}

// Delete removes a Claw agent.
func (c *Client) Delete(ctx context.Context, namespace, name string) error {
	// TODO: delete Claw CR via kubernetes API
	return fmt.Errorf("not implemented")
}

// SendMessage sends a message to a running Claw agent.
func (c *Client) SendMessage(ctx context.Context, instance *ClawInstance, message string) error {
	// TODO: send message via IPC Bus / channel
	return fmt.Errorf("not implemented")
}

// WaitForResult waits for the agent to produce a result.
func (c *Client) WaitForResult(ctx context.Context, instance *ClawInstance) (*Result, error) {
	// TODO: poll/watch for agent response
	return nil, fmt.Errorf("not implemented")
}
