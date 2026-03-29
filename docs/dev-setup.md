# Local Development Setup Guide

This guide explains how to set up your local development environment for `k8s4claw`.

## Prerequisites

Before you begin, ensure you have the following tools installed:

*   **Go**: Version 1.24 or later
*   **kubectl**: The Kubernetes command-line tool
*   **kind** or **minikube**: For running a local Kubernetes cluster
*   **make**: For running Makefile targets

## Setup `envtest`

We use `setup-envtest` for running integration tests against a local control plane.

To install and set up `envtest`:

```bash
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use $(go env GOVERSION)
```

## Build and Test

*   **Run tests**: `make test`
*   **Build the binary**: `make build`
*   **Run locally against a cluster**: `make run`
