---
title: "Building an IPC bus for Kubernetes sidecars: WAL, DLQ, and ring-buffer backpressure"
published: false
description: "How the k8s4claw IPC bus routes messages between channel sidecars and an AI agent runtime with at-least-once delivery, four wire protocols, and four layers of reliability."
tags: kubernetes, go, opensource, distributedsystems
series: "k8s4claw internals"
cover_image:
---

If you put two sidecars in a pod and ask them to talk to each other over HTTP, sooner or later one of them crashes mid-request and you lose a message. If you do it enough times, you reinvent a message bus.

This post is about the small in-pod message bus we ended up writing for [k8s4claw](https://github.com/Prismer-AI/k8s4claw), a Kubernetes operator for AI agent runtimes. The bus sits between channel sidecars (Slack, Discord, Webhook) and the agent runtime container. It has four wire protocols, a write-ahead log, a BoltDB-backed dead letter queue, and a ring buffer with backpressure. All of it is open source ([internal/ipcbus/](https://github.com/Prismer-AI/k8s4claw/tree/main/internal/ipcbus)), around 2k lines of Go.

This post is the design doc you actually want to read, not the one we had to write.

## The shape of the problem

A `Claw` pod looks like this when it has a Slack channel attached:

```text
┌──────────────────────────────────────────────┐
│  Pod                                         │
│                                              │
│  [channel-slack] ──UDS──► [ipc-bus] ──►┐     │
│                                        ▼     │
│                                  [runtime]   │
│                                              │
└──────────────────────────────────────────────┘
```

Three containers. The channel sidecar reads from Slack. The runtime is the actual AI agent. The IPC bus is a [native sidecar](https://kubernetes.io/blog/2023/08/25/native-sidecar-containers/) (init container with `restartPolicy: Always`) that routes messages between them.

The naive version of this is: let the two containers talk HTTP directly. The reality is that at least four things are going to go wrong:

1. The runtime will be overloaded when a Slack event arrives and we need somewhere to buffer it.
2. The runtime will crash mid-response and we need to redeliver.
3. A slow downstream (say, a user's laptop on 3G) will fall behind and we need to push back instead of dropping.
4. Two different runtimes we support speak four different wire protocols. HTTP isn't enough.

So we wrote a bus. Let me walk through the four mechanisms that earn their keep.

## Mechanism 1 — length-prefix framing

This isn't glamorous, but it's the first thing you get wrong in a message bus.

Every `Message` is a JSON blob on the wire:

```go
type Message struct {
    ID            string          `json:"id"`
    Type          MessageType     `json:"type"`
    Channel       string          `json:"channel,omitempty"`
    CorrelationID string          `json:"correlationId,omitempty"`
    ReplyTo       string          `json:"replyTo,omitempty"`
    Timestamp     time.Time       `json:"timestamp"`
    Payload       json.RawMessage `json:"payload,omitempty"`
}
```

On the wire it looks like `[4-byte big-endian length][JSON bytes]`:

```go
const (
    MaxMessageSize  = 16 * 1024 * 1024
    FrameHeaderSize = 4
)

func WriteMessage(w io.Writer, msg *Message) error {
    data, err := json.Marshal(msg)
    if err != nil {
        return fmt.Errorf("failed to marshal message: %w", err)
    }
    if len(data) > MaxMessageSize {
        return fmt.Errorf("message size %d exceeds maximum %d",
            len(data), MaxMessageSize)
    }

    frame := make([]byte, FrameHeaderSize+len(data))
    binary.BigEndian.PutUint32(frame, uint32(len(data)))
    copy(frame[FrameHeaderSize:], data)
    _, err = w.Write(frame)
    return err
}
```

Why length-prefix instead of newline-delimited JSON? Because JSON payloads can contain newlines inside strings and you'd have to escape them on the wire. Length-prefix framing just works: a reader reads 4 bytes, gets the length, reads that many bytes, deserializes. No lookahead, no escape tables.

The 16 MB cap is there to fail loudly rather than run out of memory on a malformed header. In practice our real messages are well under 64 KB.

## Mechanism 2 — four bridge protocols behind one interface

Different runtimes speak different things:

| Runtime    | Protocol  | Why                                              |
|------------|-----------|--------------------------------------------------|
| OpenClaw   | WebSocket | Full-duplex, JSON-native, easy from Node.js      |
| NanoClaw   | UDS       | Lowest overhead for same-pod communication       |
| ZeroClaw   | SSE       | Already has an HTTP API, SSE for server-push     |
| PicoClaw   | TCP       | Minimal client, hand-rolled in 50 lines          |

The bus abstracts them behind one interface:

```go
type RuntimeBridge interface {
    Connect(ctx context.Context) error
    Send(ctx context.Context, msg *Message) error
    Receive(ctx context.Context) (<-chan *Message, error)
    Close() error
}
```

Four methods. Adding a new protocol is one file ([example: TCP bridge](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/bridge_tcp.go)):

```go
type TCPBridge struct{ streamBridge }

func (b *TCPBridge) Connect(ctx context.Context) error {
    conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", b.addr)
    if err != nil {
        return err
    }
    b.conn = conn
    return nil
}
```

`streamBridge` is a shared base that implements `Send`/`Receive`/`Close` on top of any `net.Conn`. It handles `context.Context` deadlines properly:

```go
func (b *streamBridge) Send(ctx context.Context, msg *Message) error {
    b.mu.Lock()
    defer b.mu.Unlock()

    if b.conn == nil {
        return fmt.Errorf("not connected")
    }

    // Respect context deadline for the write.
    if deadline, ok := ctx.Deadline(); ok {
        _ = b.conn.SetWriteDeadline(deadline)
        defer func() { _ = b.conn.SetWriteDeadline(time.Time{}) }()
    }

    return WriteMessage(b.conn, msg)
}
```

The subtle bit is `Receive`. `ReadMessage` blocks on the socket. If the caller cancels the context, we want the read to unblock. So `Receive` spawns a second goroutine whose only job is to watch the context and call `Close` on the conn, which makes the blocked `ReadMessage` return with an error.

```go
go func() {
    select {
    case <-ctx.Done():
        _ = b.conn.Close()
    case <-b.closed:
    }
}()
```

The SSE bridge is the odd one out because SSE is unidirectional (server → client, event-stream format) and we need bidirectional. So it uses an HTTP POST for send and an SSE `GET /events` for receive, with exponential-backoff reconnect on the stream:

```go
backoff := time.Second
for {
    // ... connect and read events ...
    time.Sleep(backoff)
    backoff *= 2
    if backoff > 30*time.Second {
        backoff = 30 * time.Second
    }
}
```

## Mechanism 3 — Write-Ahead Log (WAL)

This is the one that earns the bus the right to exist.

When a message comes in from a channel sidecar, the router does three things:

1. Append a WAL entry to disk (emptyDir-backed) with state `pending`.
2. Call `bridge.Send(ctx, msg)` to hand it off to the runtime bridge.
3. Mark the WAL entry complete as soon as `Send` returns success. If `Send` fails, call `scheduleRetry`.

We delivery-mark on *transport success* (the bridge accepted the bytes), not on runtime ack. We considered a runtime-ack round-trip and decided against it: it doubles round-trips, forces every runtime to implement ack semantics, and our `Message.ID` is already idempotency-safe so downstream retries aren't harmful. If a message leaves `bridge.Send` OK but the runtime crashes before processing it, we lose that one message. Tradeoff: acceptable for a chat agent, *not* acceptable for a payment system. Different design calls, different bus.

`scheduleRetry` increments `Attempts` on the WAL entry. After `maxRetryAttempts = 5`, the entry is marked `dlq` and a copy is parked in the DLQ.

The WAL is a JSON-lines file. Each line is a `WALEntry`:

```go
type WALEntry struct {
    ID       string   `json:"id"`
    Channel  string   `json:"channel"`
    State    WALState `json:"state"`       // pending | complete | dlq
    Attempts int      `json:"attempts"`
    TS       string   `json:"ts"`
    Msg      *Message `json:"msg,omitempty"`
}
```

JSON-lines is nice because you can `cat wal.log | jq` during an incident and see exactly what the bus was doing. It's also append-only, which means writes are O(1) and you never corrupt the middle of the file on a crash — at worst you have a half-written last line, which the recovery code handles.

The interesting operation is compaction. The file grows without bound otherwise. Compaction rewrites the file keeping only `pending` entries:

```go
func (w *WAL) Compact() error {
    // ... write all pending entries to wal.log.tmp ...
    // atomic rename
    return os.Rename(tmpPath, w.path())
}

func (w *WAL) NeedsCompaction() bool {
    info, _ := w.file.Stat()
    return info.Size() > compactionThreshold  // 10 MB
}
```

We don't compact on every `Complete` call — that would tank throughput. The `cmd/ipcbus` binary runs a 60-second ticker that checks `NeedsCompaction()` and rewrites the file when it grows past 10 MB. That's a coarse heuristic — it will compact even if most entries are still `pending`, wasting some I/O — but it's simple and steady-state overhead is near zero. A smarter policy (also consider the `pending` ratio, pre-commit) would be a reasonable first PR.

The WAL does not fsync on every append. We batch. If a node hard-kills, we can lose the last few hundred milliseconds of messages. That's an acceptable tradeoff for a system where the upstream Slack delivery is already best-effort. If you care more about durability, `Flush()` is exposed and you can call it from your own code, but we chose not to make it automatic.

## Mechanism 4 — Dead Letter Queue (DLQ)

After 5 delivery attempts, a message is "dead." We don't silently drop it; we move it to the DLQ:

```go
func NewDLQ(path string, maxSize int, ttl time.Duration) (*DLQ, error) {
    db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
    // ...
}
```

BoltDB is [embedded KV storage with B+tree on-disk layout](https://github.com/etcd-io/bbolt). It's fast, transactional, and single-file. Perfect for a sidecar that needs a few megabytes of dead messages, queryable by ID and age.

Two eviction policies:

- **maxSize** — a hard cap on entry count. When we're full, we evict the oldest.
- **ttl** — entries older than the TTL are purged. `NewDLQ(path, maxSize, ttl)` takes both as constructor args; the `cmd/ipcbus` binary passes `maxSize=10000, ttl=24h` and runs an hourly `PurgeExpired` ticker. Library callers can pick their own.

This matters because the DLQ is the debugging surface for the bus. Something went wrong? `kubectl exec` into the sidecar, open the BoltDB file, and look at the last N entries. We've caught a couple of real bugs this way that would have been invisible with "drop on failure."

```go
func (d *DLQ) PurgeExpired() (int, error)
func (d *DLQ) Size() int
func (d *DLQ) List() ([]*DLQEntry, error)
```

Deliberately no replay-from-DLQ. If something's dead, it's dead. We want human attention, not automatic retry that hides a real problem.

## Mechanism 5 — ring buffer with backpressure

The remaining problem: what if a channel sidecar is producing faster than the runtime can consume?

Naive answer: unbounded queue. Result: OOM-killed pod.

Real answer: bounded ring buffer with high/low watermarks.

```go
func NewRingBuffer(size int, highWatermark, lowWatermark float64) *RingBuffer {
    // ... defaults to high=0.8, low=0.3 ...
}
```

When the buffer fills past 80%, the bus emits a `slow_down` control message upstream. The channel sidecar sees it and stops pulling from Slack. When the buffer drains below 30%, the bus emits `resume` and the sidecar starts pulling again.

Why two watermarks? Because if you use one, you thrash. Right at the threshold, every push flips state. Two watermarks with a gap gives you hysteresis. Classic control-theory stuff, very little Go stuff.

The `slow_down` / `resume` messages ride the same wire format as everything else:

```go
switch m.Type {
case TypeAck, TypeNack, TypeSlowDown, TypeResume,
     TypeShutdown, TypeRegister, TypeHeartbeat:
    return true
}
```

Treating control traffic as just another `MessageType` means channel sidecars don't need a separate control channel. One TCP/UDS/WS connection carries both payloads and backpressure signals. Simpler, fewer failure modes.

## Shutdown

Graceful shutdown is its own hazard. On SIGTERM the `cmd/ipcbus` binary runs a local `shutdown()` helper that does the bare minimum:

```go
func shutdown(logger, router, wal, bridge, cancel) {
    router.SendShutdown()      // tell sidecars we're going away
    time.Sleep(5 * time.Second) // fixed grace window
    wal.Flush()                 // flush WAL to disk
    bridge.Close()              // close the runtime bridge
    cancel()                    // stop the UDS server + background tickets
}
```

That's it. No polling, no early exit if sidecars disconnect, no DLQ close (process-exit flushes BoltDB's mmap and that's enough). Whatever is still `pending` in the WAL when we exit gets replayed on next startup — that's the whole point of the WAL.

There's also a fancier `ShutdownOrchestrator` in [`internal/ipcbus/shutdown.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/shutdown.go) that takes a `drainTimeout` parameter and polls `router.ConnectedCount()` every 100 ms to exit early, but the current binary doesn't wire it up. Good first PR: swap the local helper out for the orchestrator so the sleep becomes a real wait-for-drain.

## What we didn't do (on purpose)

- **Multi-pod clustering.** The bus is deliberately in-pod. If you want cross-pod messaging, use a real broker (NATS, Redis streams). Scoping this to one pod kept us sane.
- **Ordering guarantees across channels.** Within one channel, messages are ordered. Across channels, no promise. Most agent workloads don't care.
- **Exactly-once.** At-least-once with idempotent consumers is simpler and good enough. The runtime is expected to deduplicate on `Message.ID`.
- **Protobuf on the wire.** JSON is ~2× larger but 10× easier to debug. Given our throughput (tens of messages per second per pod, not millions), JSON is the right call.

## Testing

We aimed for >80% statement coverage on the ipcbus package, approximately. The non-obvious piece: most of the reliability features are hard to unit-test with mocks because they're about failure modes. So we have a lot of tests that spin up real local listeners (`net.Listen("tcp", "127.0.0.1:0")`, `net.Listen("unix", t.TempDir()+"/sock")`, `httptest.NewServer(...)`) and exercise the bridges end-to-end.

For example, the SSE bridge test spins up an `httptest` server that handles both `GET /events` (as an SSE stream) and `POST /messages`, and checks that connecting, sending, and receiving all work:

```go
func TestSSEBridge_SendReceive(t *testing.T) {
    srv, ready := sseEchoServer(t)
    defer srv.Close()

    bridge := NewSSEBridge(srv.URL)
    // ... connect, wait for SSE stream to establish, send, receive ...
}
```

About 70 tests total, `-race` clean. Good enough for a sidecar.

## What this bought us

A uniform contract for channel sidecars. You write one Slack sidecar, it works with every runtime. You write one Discord sidecar, same thing. Runtime authors pick a protocol that fits their stack; they don't think about durability, retries, or backpressure — the bus handles it.

The runtime adapter for a new protocol is ~50 lines. The channel sidecar SDK ([`sdk/channel/`](https://github.com/Prismer-AI/k8s4claw/tree/main/sdk/channel)) hides the framing entirely; you call `client.Send(ctx, json.RawMessage(...))` and move on.

The whole ipcbus package is ~2k lines of Go. If you want to read one file to get the flavor, [`router.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/router.go) is where all five mechanisms meet.

## What to look at next

- The [k8s4claw repo](https://github.com/Prismer-AI/k8s4claw) if you want to use it
- [`internal/ipcbus/`](https://github.com/Prismer-AI/k8s4claw/tree/main/internal/ipcbus) if you want to read the code
- [The intro post](https://dev.to/willamhou/k8s4claw-a-kubernetes-operator-for-managing-ai-agent-runtimes-3anm) if you want context on how this fits into the operator

Open source, Apache-2.0. Questions and PRs welcome. If you've built something similar and went in a different direction, I'd love to hear why in the comments.
