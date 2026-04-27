---
platform: Twitter / X
purpose: Drive traffic to the Auto-update controller Dev.to deep-dive (Post 3 in k8s4claw internals series)
status: draft
---

# Twitter/X thread — Auto-update controller deep-dive promo

Three tweets. All under 280 chars (X counts each URL as 23 chars regardless of length).

## Tweet 1/3 (anchor — carries the link)

```text
New post: how the auto-update controller in k8s4claw works.

One annotation drives the rollout. Cron-driven OCI tag polling, semver-filtered. Health gate on UpdatedReplicas + ReadyReplicas. Auto-rollback on timeout. Circuit breaker after 3 strikes.

https://dev.to/willamhou/auto-updating-kubernetes-workloads-an-annotation-driven-rollout-with-circuit-breaker
```

## Tweet 2/3 (technical hook)

```text
Subtle bit: the controller doesn't patch the StatefulSet.

It flips claw.prismer.ai/target-image and lets the main reconciler pick that up when it rebuilds the pod template. Rollback = delete the annotation.

In-flight state lives on the resource. Controller has no memory.
```

## Tweet 3/3 (CTA + repo)

```text
~470 lines of Go. Fake clock + fake tag lister make the tests deterministic. Three test files, sub-second per run.

Series: https://dev.to/willamhou
Repo: https://github.com/Prismer-AI/k8s4claw

#Kubernetes #Golang #DistributedSystems
```

## Character-count sanity check

X/Twitter counts every URL as exactly 23 characters (t.co shortening), so the raw text above can be under-counted when you paste it — trust the in-composer counter. Everything here should show < 280 in the composer.

## Posting checklist

- [ ] Verify each tweet shows < 280 in the composer before clicking post.
- [ ] Post as a proper thread (click "Add another tweet"), not three standalone tweets.
- [ ] Pin tweet 1 to profile for the next 48 hours.
- [ ] Monitor replies for the first hour — algorithm rewards fast engagement.
- [ ] Replace the Dev.to URL in tweet 1 with the actual published URL once the post goes live (this draft uses a placeholder slug).
