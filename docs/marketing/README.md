# claw4k8s Marketing Assets

Quick reference for launching claw4k8s publicly.

## Files

- **[demo-storyboard.md](demo-storyboard.md)** — 2-minute demo storyboard, asciinema recording commands, Twitter thread, HN submission copy
- **[../../scripts/demo-claw4k8s.sh](../../scripts/demo-claw4k8s.sh)** — Executable demo script for asciinema capture

## Launch checklist

### Pre-launch (30 min)
- [ ] Run `./scripts/demo-claw4k8s.sh` locally to verify it completes cleanly
- [ ] Record: `asciinema rec demo.cast -c ./scripts/demo-claw4k8s.sh`
- [ ] Generate GIF: `agg demo.cast demo.gif --speed 1.5 --theme monokai --font-size 18`
- [ ] Upload demo.cast to asciinema.org — note the public URL
- [ ] Upload demo.gif to GitHub release / imgur for Twitter embed

### README update (15 min)
- [ ] Add "Self-healing AI agents" section to root README with embedded GIF
- [ ] Add claw4k8s link to features list
- [ ] Add comparison table vs k8sgpt / kubectl-ai / Holmes

### Launch (Tuesday/Wednesday 9am PT — peak HN window)
- [ ] Post Tweet 1 with GIF
- [ ] Submit to HN (Show HN)
- [ ] Cross-post to r/kubernetes, r/devops
- [ ] Message 3–5 K8s-operator-space folks for feedback/amplification

### Post-launch (same day)
- [ ] Monitor HN thread, respond to all comments within 1 hour
- [ ] Pin top Twitter thread to profile
- [ ] Add launch traffic bump to GitHub stars tracking

## Positioning one-liner

> "The first Kubernetes operator where AI agents manage their own infrastructure."

## Anti-positioning (what we're NOT)

- NOT another kubectl wrapper (kubectl-ai does that)
- NOT a diagnostics-only tool (k8sgpt does that)
- NOT an SRE co-pilot for general K8s (Holmes does that, for now)

## Unique wedges

1. **Dogfooding as the product** — the agent runs its own infra, not a separate SRE use case
2. **Intent annotation pattern** — architecturally novel, prevents controller contention and prompt injection
3. **Graceful degradation** — LLM isn't a hard dependency; system falls back to notification mode
4. **Already shipped K8s primitives** — channels, persistence, RBAC, IPC bus — this isn't a toy
