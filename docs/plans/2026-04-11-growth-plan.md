# k8s4claw Growth & Distribution Plan

Created: 2026-04-11 · Last updated: 2026-04-23

## Current Baseline (2026-04-23)

| Metric | Value |
|--------|-------|
| Stars | 7 |
| Forks | 4 |
| External Contributors | 3 |
| Commits | 140+ |
| Release | v0.1.0 |
| Container Images | 7 on GHCR (all public) |
| Demo | SVG + MP4 (Docker + K8s) |
| Codespaces | One-click kind cluster |
| Published Articles | 2 on Dev.to, 2 on Zhihu, 2 on Juejin (intro + IPC bus deep-dive) |
| Article Drafts | `docs/articles/` (6 files total — EN/ZH × 3 posts) |

## Published Articles

| Post | EN (Dev.to) | ZH (Zhihu) | ZH (Juejin) | Status |
|------|-------------|------------|-------------|--------|
| **1. Intro: One CRD for AI agent runtimes** | [🔗 live](https://dev.to/willamhou/k8s4claw-a-kubernetes-operator-for-managing-ai-agent-runtimes-3anm) | ✅ published | ✅ published | Day 1 (2026-04-21) |
| **2. IPC bus deep-dive** | [🔗 live](https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27) | ✅ published | draft on disk | Day 3 (2026-04-23) |
| **3. Auto-update controller deep-dive** | ✅ published | ✅ published | ✅ published | Day 7 (2026-04-27) |

Both Dev.to posts carry `series: "k8s4claw internals"` so they nav-link on the site.

## Core Message

**One-liner:** One `kubectl apply` to deploy an AI agent on Kubernetes.

**Hook:** Your team has 5 AI bots scattered across Lambda, EC2, and someone's laptop. k8s4claw lets you manage them like microservices.

**CTA:** `github.com/Prismer-AI/k8s4claw` — Open in Codespaces, 3 minutes to try.

---

## Week 1: Content Launch

| Day | Action | Platform | Status |
|-----|--------|----------|--------|
| Day 1 (2026-04-21) | Publish intro article | Dev.to EN | ✅ live |
| Day 1 (2026-04-21) | Publish intro article | 知乎 + 掘金 | ✅ published |
| Day 1 (2026-04-21) | Publish Twitter thread | Twitter/X | ✅ posted |
| Day 3 (2026-04-23) | Publish IPC bus deep-dive | Dev.to EN | ✅ live |
| Day 3 (2026-04-23) | Publish IPC bus deep-dive | 知乎 | ✅ published |
| Day 3 (2026-04-23) | Publish IPC bus deep-dive | 掘金 | ⏳ draft on disk |
| Day 3 (2026-04-23) | Twitter thread (EN) for IPC bus post | Twitter/X | ✅ posted |
| Day 3 (2026-04-23) | 中文 thread for IPC bus post | 微博/即刻/中文 X | ✅ posted |
| Day 2/3 | Show HN | Hacker News | ⏳ not posted yet |
| Day 2/3 | Post to r/kubernetes + r/selfhosted | Reddit | ⏳ not posted yet |
| Day 3 | LinkedIn long post | LinkedIn | ⏳ not posted yet |

### What's left in Week 1

Biggest unshipped channel is **Show HN** — plan says Day 2, we're on Day 3. HN posts at US Pacific 7–9am (Beijing 10pm–midnight) get the best first-hour bump. Use the Dev.to intro URL as the HN link target. Second most valuable: **Reddit r/kubernetes**; templates below.

### Show HN Template (updated 2026-04-23)

Target URL: `https://github.com/Prismer-AI/k8s4claw`
Title: `Show HN: k8s4claw – Kubernetes operator for AI agent runtimes`

First comment (post it immediately after submission, it makes or breaks the HN window):

```
Author here. Built this because we had 7 different AI agent runtimes on K8s,
each with its own Helm chart and sidecar layout. Rolling back a bad update
was manual.

k8s4claw reconciles a full stack from one Claw CR: StatefulSet, Service,
ConfigMap, PDB, NetworkPolicy. When you add a channel (Slack/Discord/Webhook),
it injects a sidecar and an IPC bus (WAL + DLQ + ring-buffer backpressure)
behind four wire protocols (WS/TCP/UDS/SSE).

Auto-update controller polls OCI registries on cron, filters by semver, does
health-verified rollouts with circuit-breaker rollback.

Intro: https://dev.to/willamhou/k8s4claw-a-kubernetes-operator-for-managing-ai-agent-runtimes-3anm
IPC bus internals: https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27

Happy to answer questions about the IPC bus design, the runtime adapter pattern,
or why the update state machine lives in annotations.
```

### Twitter Thread Template

```
1/ We built k8s4claw — a Kubernetes operator for AI agent runtimes.

One CRD. Any runtime. Production-ready from day one.

🧵 Thread: why we built it and how it works →

2/ The problem: 5 AI bots scattered across Lambda, EC2, and someone's laptop.
No unified management. No auto-updates. No observability.

k8s4claw wraps it all into a single `kubectl apply`.

3/ What you get per agent:
- StatefulSet + Service
- IPC Bus (WAL + DLQ + backpressure)
- Channel sidecars (Slack, Discord, Webhook)
- Auto-update with health checks + circuit breaker
- PVC persistence + CSI snapshots

4/ Try it in 3 minutes — no setup needed:
[Open in Codespaces badge link]

Or locally:
docker run -p 18900:18900 -e OPENCLAW_MODE=mock ghcr.io/prismer-ai/k8s4claw-openclaw:0.1.0

5/ Open source (Apache-2.0). Looking for contributors!

GitHub: github.com/Prismer-AI/k8s4claw
Demo: [demo-k8s.mp4 link]

Good first issues waiting for you 👋
```

### Reddit Post Template (updated 2026-04-23)

```
Title: k8s4claw: Kubernetes operator for managing AI agent runtimes

We open-sourced k8s4claw — a K8s operator that manages heterogeneous AI agent
runtimes with a single CRD.

**What it does:**
- 7 built-in runtime adapters (OpenClaw, NanoClaw, ZeroClaw, PicoClaw,
  IronClaw, HermesClaw, K8sOps)
- IPC Bus sidecar with WAL, DLQ, and ring-buffer backpressure
- Channel sidecars (Slack, Discord, Webhook)
- Auto-update with semver filtering + circuit-breaker rollback
- Helm chart with cert-manager integration

**Try it:** Open in GitHub Codespaces — kind cluster auto-provisioned, 3 min
to first `kubectl get claws`.

**Demo video (80s):** https://github.com/Prismer-AI/k8s4claw/releases/download/v0.1.0/demo-k8s.mp4
**Writeup:** https://dev.to/willamhou/k8s4claw-a-kubernetes-operator-for-managing-ai-agent-runtimes-3anm
**IPC bus internals:** https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27

GitHub: https://github.com/Prismer-AI/k8s4claw

Looking for feedback on the IPC bus design and the runtime adapter pattern.
```

---

## Week 2: Community Outreach

| Action | Where | Status |
|--------|-------|--------|
| Show HN | Hacker News | ⏳ pending (high priority) |
| Post to r/kubernetes | Reddit | ⏳ pending |
| Post to r/selfhosted | Reddit | ⏳ pending |
| LinkedIn long post | LinkedIn | ⏳ pending |
| Post intro + demo link | CNCF Slack #kubernetes-operators | ⏳ |
| Post intro + demo link | Kubernetes Slack #general | ⏳ |
| Mention Hermes integration | NousResearch Discord | Reference Issue #10 |
| Submit to awesome-kubernetes | GitHub PR | ✅ submitted |
| Submit to awesome-agents / awesome-ai-agents-2026 | GitHub PR | ✅ 5 submitted, waiting on review |
| Cross-post intro to 微信公众号 | Chinese tech community | ⏳ |
| Publish IPC bus deep-dive to 掘金 | Juejin | ⏳ draft on disk |

---

## Week 3: Interactive Experience

| Action | Notes |
|--------|-------|
| Create Killercoda tutorial | Browser-based, no local setup |
| Product Hunt launch | Non-technical audience |
| Record YouTube video (3-5 min) | Demo + architecture walkthrough |
| Submit to CNCF Landscape | AI/ML category |

---

## Monthly Recurring

| Action | Frequency |
|--------|-----------|
| Publish changelog / update post | Per release |
| Engage with issues and PRs | Daily |
| Share contributor spotlight | Bi-weekly |
| Answer K8s + AI questions on Reddit/SO | Weekly |
| Update awesome lists if new features | Per release |

---

## Tracking

| Metric | Baseline (04-11) | Now (04-23) | Week 1 Target | Month 1 Target |
|--------|------------------|-------------|---------------|----------------|
| GitHub Stars | 6 | 7 | 50 | 200 |
| Forks | 4 | 4 | 15 | 40 |
| External PRs merged | 3 | 4 | 5 | 15 |
| Articles published | 0 | 5 (2 DevTo + 2 知乎 + 1 掘金) | — | — |
| Social threads posted | 0 | 2 (EN + CN for IPC bus post) | — | — |
| HN points | — | — (not yet posted) | 30 | — |

---

## Assets Ready

- [x] Dev.to intro article: `docs/articles/devto-introducing-k8s4claw.md` — **published**
- [x] Dev.to IPC bus deep-dive: `docs/articles/devto-ipc-bus-internals.md` — **published**
- [x] Zhihu intro: `docs/articles/zhihu-introducing-k8s4claw.md` — **published**
- [x] Zhihu IPC bus: `docs/articles/zhihu-ipc-bus-internals.md` — **published**
- [x] Juejin intro: `docs/articles/juejin-introducing-k8s4claw.md` — **published**
- [x] Juejin IPC bus: `docs/articles/juejin-ipc-bus-internals.md` — draft on disk, ready to publish
- [x] Both Dev.to posts use `series: "k8s4claw internals"` for nav-linking
- [x] Demo video (Docker): `docs/demo.mp4` (833KB, 31s)
- [x] Demo video (K8s): `docs/demo-k8s.mp4` (1.7MB, ~80s)
- [x] Demo SVG (animated): `docs/demo-k8s.svg`
- [x] Zhihu cover candidates: `/tmp/zhihu-cover-*.png` (900×500)
- [x] Codespaces config: `.devcontainer/`
- [x] Quick start guide: `config/samples/quickstart/README.md`
- [x] Twitter thread (EN) file: `docs/articles/twitter-thread-ipc-bus.md` — **posted 2026-04-23**
- [x] Twitter/Weibo thread (CN) file: `docs/articles/weibo-thread-ipc-bus.md` — **posted 2026-04-23**
- [x] Dev.to Post 3 (Auto-update): `docs/articles/devto-autoupdate-internals.md` — **published 2026-04-27**
- [x] Zhihu Post 3 (Auto-update): `docs/articles/zhihu-autoupdate-internals.md` — **published 2026-04-27**
- [x] Juejin Post 3 (Auto-update): `docs/articles/juejin-autoupdate-internals.md` — **published 2026-04-27**
- [x] Twitter thread (EN) Post 3: `docs/articles/twitter-thread-autoupdate.md` — **posted 2026-04-27**
- [x] Twitter/Weibo thread (CN) Post 3: `docs/articles/weibo-thread-autoupdate.md` — **posted 2026-04-27**
- [x] Show HN template: in this file above (updated 2026-04-23)
- [x] Reddit template: in this file above (updated 2026-04-23)
- [x] Good first issues: #4
- [ ] Juejin IPC bus publish (last of 6)
- [ ] Show HN post (biggest remaining lever)
- [ ] Reddit r/kubernetes + r/selfhosted posts
- [ ] LinkedIn long post
- [ ] Killercoda tutorial
- [ ] YouTube video

## Next Article Ideas (post-IPC-bus series)

Ordered by ROI for attracting contributors:

1. **Go K8s Operator 7 个坑** — Juejin-first Chinese tutorial. Controller-runtime + envtest gotchas.
2. **HermesClaw adapter contributor recap** — Dev.to. How PR #11 landed. Sales pitch to new contributors.
3. **Auto-update controller state machine** — Dev.to. Circuit breaker + annotations-based state machine.
4. **claw4k8s: why the LLM never has kubectl** — Dev.to. Sells the security wedge.

All four have full source material in the repo.
