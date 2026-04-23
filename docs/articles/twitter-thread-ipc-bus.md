---
platform: Twitter / X
purpose: Drive traffic to the IPC bus Dev.to deep-dive (Post 2 in k8s4claw internals series)
target_post_time: Beijing 22:00–24:00 (US Pacific 07:00–09:00)
---

# Twitter/X thread — IPC bus deep-dive promo

Three tweets. All under 280 chars (X counts each URL as 23 chars regardless of length).

Revised 2026-04-23 after codex review flagged: tweet 1 was 396 chars, tweet 2 was 281 chars, "at-least-once delivery" in tweet 1 contradicted tweet 2's "complete on bridge.Send", and "doubles latency" was a stronger claim than the article's "doubles round-trips."

## Tweet 1/3 (anchor — carries the link, ~219 chars with URL counted as 23)

```text
New post: how the in-pod IPC bus in k8s4claw works.

Four wire protocols (WS/TCP/UDS/SSE) behind one 4-method interface. WAL on disk for crash recovery. BoltDB dead letter queue. Ring buffer with hysteresis for backpressure.

https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27
```

## Tweet 2/3 (technical hook — ~254 chars)

```text
Subtle bit: the WAL marks a message complete on bridge.Send() success, not on runtime ack.

A runtime-ack round-trip doubles round-trips and forces every runtime to implement ack semantics. Message.ID gives consumers a dedupe key instead.

Tradeoff we chose knowingly.
```

## Tweet 3/3 (CTA + repo — ~248 chars with URLs)

```text
~2k lines of Go. ~80% covered. The reliability tests spin up real local listeners instead of mocks because failure modes don't mock well.

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
- [ ] Do NOT @-mention big accounts cold in the thread; wait for one to engage organically.
- [ ] If Dev.to has a cleaner series URL than `/willamhou`, use it in tweet 3.
