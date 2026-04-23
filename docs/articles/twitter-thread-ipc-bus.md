---
platform: Twitter / X
purpose: Drive traffic to the IPC bus Dev.to deep-dive (Post 2 in k8s4claw internals series)
target_post_time: Beijing 22:00–24:00 (US Pacific 07:00–09:00)
---

# Twitter/X thread — IPC bus deep-dive promo

Three tweets. First tweet carries the main link; the follow-ups add a sharp technical hook and a closing CTA.

## Tweet 1/3 (anchor — has the link)

```
Part 2 of k8s4claw internals is up: how the in-pod IPC bus between channel sidecars and the AI runtime actually works.

WAL for at-least-once delivery. BoltDB-backed DLQ. Ring buffer with hysteresis for backpressure. Four wire protocols (WS/TCP/UDS/SSE) behind one 4-method interface.

https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27
```

## Tweet 2/3 (technical hook — spicy claim)

```
The subtle bit: the WAL marks a message complete on bridge.Send() success, not on runtime ack. We considered the round-trip and said no — doubles latency, forces every runtime to implement ack semantics, and Message.ID is already idempotent.

Tradeoff we chose knowingly. Not free.
```

## Tweet 3/3 (CTA + repo)

```
2k lines of Go. ~80% covered. A lot of the reliability tests spin up real local listeners instead of mocks because failure modes don't mock well.

Series: https://dev.to/willamhou
Repo: https://github.com/Prismer-AI/k8s4claw

#Kubernetes #Golang #DistributedSystems
```

## Posting checklist

- [ ] Replace `https://dev.to/willamhou` in tweet 3 with a series landing URL if Dev.to provides one (otherwise leave as author page — it shows the series nav bar).
- [ ] Post as a proper thread (click "Add another tweet"), not three standalone tweets.
- [ ] Pin tweet 1 to profile for the next 48 hours.
- [ ] Monitor replies for the first hour — algorithm rewards fast engagement.
- [ ] Do NOT @-mention big accounts cold in the thread; wait for one to engage organically.
