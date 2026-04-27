// kubectl-claw is a kubectl plugin for k8s4claw operations.
//
// Subcommands:
//
//	approve <escalation>    Mark a ClawOpsEscalation as Approved.
//	reject  <escalation>    Mark a ClawOpsEscalation as Rejected.
//
// Install: place this binary on $PATH as `kubectl-claw`. Then run
// `kubectl claw approve <name>`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const usage = `kubectl-claw — operator-side approval CLI for k8s4claw

Usage:
  kubectl claw approve <escalation> [-n namespace] [--by user@example.com]
  kubectl claw reject  <escalation> [-n namespace] [--reason "..."]

Examples:
  kubectl claw approve my-claw-ops-abc -n ai-agents --by sre@corp.com
  kubectl claw reject  my-claw-ops-xyz -n default   --reason "manual rollback already done"
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "approve":
		os.Exit(runApprove(args))
	case "reject":
		os.Exit(runReject(args))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", cmd)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// commonFlags returns the parsed name + namespace + extra arg.
func commonFlags(args []string, extraName string) (name, namespace, extra string, err error) {
	fs := flag.NewFlagSet(extraName, flag.ContinueOnError)
	fs.StringVar(&namespace, "n", "default", "namespace")
	fs.StringVar(&namespace, "namespace", "default", "namespace")
	fs.StringVar(&extra, extraName, "", extraName+" annotation")
	if err := fs.Parse(args); err != nil {
		return "", "", "", err
	}
	if fs.NArg() < 1 {
		return "", "", "", fmt.Errorf("missing escalation name")
	}
	return fs.Arg(0), namespace, extra, nil
}

func newClient() (client.Client, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	if err := v1alpha1.AddToScheme(scheme.Scheme); err != nil {
		return nil, fmt.Errorf("failed to register scheme: %w", err)
	}
	return client.New(cfg, client.Options{Scheme: scheme.Scheme})
}

func runApprove(args []string) int {
	name, ns, by, err := commonFlags(args, "by")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if by == "" {
		// Default to whoami-style identity from KUBECONFIG context.
		by = currentUser()
	}

	c, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx := context.Background()

	var esc v1alpha1.ClawOpsEscalation
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &esc); err != nil {
		fmt.Fprintf(os.Stderr, "failed to get escalation %s/%s: %v\n", ns, name, err)
		return 1
	}

	if esc.Status.Phase != v1alpha1.EscalationPhaseAwaitingApproval {
		fmt.Fprintf(os.Stderr, "escalation %s is in phase %q (must be %q to approve)\n",
			name, esc.Status.Phase, v1alpha1.EscalationPhaseAwaitingApproval)
		return 1
	}
	if esc.Status.ProposedAction == "" {
		fmt.Fprintf(os.Stderr, "escalation %s has empty proposedAction — nothing to approve\n", name)
		return 1
	}

	now := metav1.Now()
	esc.Status.Phase = v1alpha1.EscalationPhaseApproved
	esc.Status.ApprovedBy = by
	esc.Status.ApprovedAt = &now

	if err := c.Status().Update(ctx, &esc); err != nil {
		fmt.Fprintf(os.Stderr, "failed to update status: %v\n", err)
		return 1
	}
	fmt.Printf("approved %s/%s by %s\nproposedAction will be executed by ClawOpsController\n", ns, name, by)
	return 0
}

func runReject(args []string) int {
	name, ns, reason, err := commonFlags(args, "reason")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if reason == "" {
		reason = "rejected via kubectl-claw"
	}

	c, err := newClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx := context.Background()

	var esc v1alpha1.ClawOpsEscalation
	if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: ns}, &esc); err != nil {
		fmt.Fprintf(os.Stderr, "failed to get escalation %s/%s: %v\n", ns, name, err)
		return 1
	}

	if v1alpha1.IsTerminalPhase(esc.Status.Phase) {
		fmt.Fprintf(os.Stderr, "escalation %s is already terminal (phase=%q)\n", name, esc.Status.Phase)
		return 1
	}

	esc.Status.Phase = v1alpha1.EscalationPhaseRejected
	esc.Status.RejectionReason = reason

	if err := c.Status().Update(ctx, &esc); err != nil {
		fmt.Fprintf(os.Stderr, "failed to update status: %v\n", err)
		return 1
	}
	fmt.Printf("rejected %s/%s: %s\n", ns, name, reason)
	return 0
}

// currentUser returns a best-effort identity string for the approval audit trail.
// Falls back to USER env var, then "unknown".
func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}
