---
title: "给 K8s Sidecar 写一个消息总线：WAL、DLQ、和环形缓冲背压"
platform: 知乎
tags: Kubernetes, Go, 开源, 分布式, 消息队列
---

# 给 K8s Sidecar 写一个消息总线：WAL、DLQ、和环形缓冲背压

> 本文讲的是 [k8s4claw](https://github.com/Prismer-AI/k8s4claw) 项目里的 `ipcbus` 包——一个在 Pod 内部跑的小型消息总线，约 2k 行 Go。四种传输协议、WAL 持久化、BoltDB 死信队列、环形缓冲背压。本文是"你实际会想读的那种设计文档"，不是那种为了合规被迫写的。

在一个 Pod 里塞两个 sidecar 让它们走 HTTP 说话，迟早会遇到：某一个在处理请求的中途崩了，消息丢了。次数多了，你就会发现自己在重新发明一个消息总线。

## 问题的形状

k8s4claw 的 `Claw` Pod 带上 Slack channel 后长这样：

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

三个容器：channel sidecar 从 Slack 拉消息，runtime 是真正的 AI agent，IPC Bus 是一个 [K8s native sidecar](https://kubernetes.io/blog/2023/08/25/native-sidecar-containers/)（`restartPolicy: Always` 的 init 容器），在它们之间路由消息。

朴素的做法：让两个容器直接走 HTTP。现实：至少 4 件事会出问题：

1. Slack 事件到达时 runtime 过载，需要缓冲。
2. runtime 中途崩了，需要重投。
3. 下游慢（比如用户笔记本连 3G）导致生产者堆积，需要背压而不是丢消息。
4. 我们支持的几种 runtime 说四种不同的协议。HTTP 不够用。

于是写了这个 bus。下面讲五个真正有用的机制。

## 机制 1 — 长度前缀帧

不够花哨，但消息总线第一件会搞错的事。

每个 `Message` 在网线上是 JSON：

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

线上格式：`[4 字节大端长度][JSON 字节]`：

```go
const (
    MaxMessageSize  = 16 * 1024 * 1024
    FrameHeaderSize = 4
)

func WriteMessage(w io.Writer, msg *Message) error {
    data, _ := json.Marshal(msg)
    if len(data) > MaxMessageSize {
        return fmt.Errorf("message size %d exceeds maximum %d", len(data), MaxMessageSize)
    }
    frame := make([]byte, FrameHeaderSize+len(data))
    binary.BigEndian.PutUint32(frame, uint32(len(data)))
    copy(frame[FrameHeaderSize:], data)
    _, err := w.Write(frame)
    return err
}
```

为什么用长度前缀不用换行分隔 JSON？因为 JSON 字符串里可以含换行，你得在网线上转义。长度前缀直接解决：读 4 字节拿长度，再读那么多字节反序列化。无前瞻、无转义表。

16 MB 上限是为了在遇到损坏的 header 时快速失败，而不是把内存吃爆。实际上我们真实消息都在 64 KB 以下。

## 机制 2 — 一个接口，四个桥接协议

不同 runtime 说不同的话：

| Runtime    | 协议      | 原因                                   |
|------------|-----------|----------------------------------------|
| OpenClaw   | WebSocket | 全双工、JSON 原生、Node.js 友好        |
| NanoClaw   | UDS       | 同 Pod 通信开销最低                    |
| ZeroClaw   | SSE       | 已经有 HTTP API，服务端推用 SSE        |
| PicoClaw   | TCP       | 客户端极简，50 行自己写                |

Bus 通过一个接口把它们抽象掉：

```go
type RuntimeBridge interface {
    Connect(ctx context.Context) error
    Send(ctx context.Context, msg *Message) error
    Receive(ctx context.Context) (<-chan *Message, error)
    Close() error
}
```

四个方法。加一种新协议就是一个文件（[TCP bridge 例子](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/bridge_tcp.go)）：

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

`streamBridge` 是一个共享基础类型，在任意 `net.Conn` 之上实现 `Send`/`Receive`/`Close`。它正确处理 `context.Context` 的 deadline：

```go
func (b *streamBridge) Send(ctx context.Context, msg *Message) error {
    b.mu.Lock()
    defer b.mu.Unlock()

    if b.conn == nil {
        return fmt.Errorf("not connected")
    }
    if deadline, ok := ctx.Deadline(); ok {
        _ = b.conn.SetWriteDeadline(deadline)
        defer func() { _ = b.conn.SetWriteDeadline(time.Time{}) }()
    }
    return WriteMessage(b.conn, msg)
}
```

微妙的地方在 `Receive`。`ReadMessage` 会阻塞在 socket 上。如果调用方 cancel 了 context，我们要让这个 read 立即解除阻塞。所以 `Receive` 启动一个第二协程，专门盯着 context，context 取消时调用 `conn.Close()`，从而让阻塞的 `ReadMessage` 立即返回错误：

```go
go func() {
    select {
    case <-ctx.Done():
        _ = b.conn.Close()
    case <-b.closed:
    }
}()
```

SSE 桥最特殊，因为 SSE 是单向的（server → client，event-stream 格式），但我们需要双向。所以它用 HTTP POST 做 send，用 SSE `GET /events` 做 receive，流断开时指数退避重连：

```go
backoff := time.Second
for {
    // ... 连接并读 events ...
    time.Sleep(backoff)
    backoff *= 2
    if backoff > 30*time.Second {
        backoff = 30 * time.Second
    }
}
```

## 机制 3 — Write-Ahead Log (WAL)

这个机制是整个 bus 存在的意义。

channel sidecar 发来一条消息后，router 做三件事：

1. 写一条 `pending` 状态的 WAL 记录到磁盘（emptyDir 支撑）。
2. 调用 `bridge.Send(ctx, msg)` 把消息交给 runtime bridge。
3. 只要 `Send` 返回成功就立刻标 complete；失败则走 `scheduleRetry`。

**按"传输成功"标完成，不按"runtime ack"**。我们考虑过 runtime ack 的往返，最后没做：它让往返翻倍，每个 runtime 都要实现 ack 语义，而且我们的 `Message.ID` 本身就支持幂等——下游重试是安全的。如果消息顺利离开 `bridge.Send` 后 runtime 处理前就崩了，这一条我们会丢。取舍：对聊天 agent 可以接受，**对支付系统不行**。不同系统，不同的总线。

`scheduleRetry` 会把 WAL 记录的 `Attempts` 加 1。达到 `maxRetryAttempts = 5` 后标记为 `dlq`，副本进 DLQ。

WAL 是一个 JSON-lines 文件，每行是一条 `WALEntry`：

```go
type WALEntry struct {
    ID       string   `json:"id"`
    Channel  string   `json:"channel"`
    State    WALState `json:"state"`      // pending | complete | dlq
    Attempts int      `json:"attempts"`
    TS       string   `json:"ts"`
    Msg      *Message `json:"msg,omitempty"`
}
```

用 JSON-lines 的好处是：事故排查时 `cat wal.log | jq` 就能看到 bus 当时在干什么。它也是 append-only，写是 O(1)，崩了也不会破坏文件中间——最多是最后一行写了一半，recovery 代码会处理。

有意思的操作是 **compaction**。文件会无限增长，所以要重写。compaction 只保留 `pending` 记录：

```go
func (w *WAL) Compact() error {
    // ... 把所有 pending entry 写到 wal.log.tmp ...
    return os.Rename(tmpPath, w.path())
}

func (w *WAL) NeedsCompaction() bool {
    info, _ := w.file.Stat()
    return info.Size() > compactionThreshold // 10 MB
}
```

我们不会每次 `Complete` 都 compact——那样吞吐会崩。`cmd/ipcbus` 二进制里有个 60 秒 ticker 调 `NeedsCompaction()`，文件超过 10 MB 就重写。判据粗糙——即使大部分还是 `pending` 也会 compact，浪费一点 IO——但简单、稳态开销接近 0。如果想做更聪明的策略（先看 `pending` 比例再决定要不要写），这是个不错的第一个 PR 切入点。

WAL 的每次 append 不做 fsync。我们做批处理。如果节点硬挂，会丢最后几百毫秒的消息。对我们来说这是可接受的折中——上游 Slack 投递本来就是尽力而为的。如果你更在意持久性，`Flush()` 是暴露出来的，你可以自己调，但我们没做自动化。

## 机制 4 — Dead Letter Queue (DLQ)

一条消息重试 5 次失败后就是"死"的。我们不静默丢弃，而是放进 DLQ：

```go
func NewDLQ(path string, maxSize int, ttl time.Duration) (*DLQ, error) {
    db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 1 * time.Second})
    // ...
}
```

BoltDB 是 [B+tree 嵌入式 KV](https://github.com/etcd-io/bbolt)。快、事务性、单文件。对一个需要几 MB 死消息、能按 ID 和时间查询的 sidecar 来说完美。

两种淘汰策略：

- **maxSize**：条目数硬上限。满了就淘汰最旧的。
- **ttl**：超过 TTL 的记录被清理。`NewDLQ(path, maxSize, ttl)` 两个参数都在构造函数里；`cmd/ipcbus` 二进制默认传 `maxSize=10000, ttl=24h` 并跑一个小时一次的 `PurgeExpired` ticker。库的使用者可以自己选。

这事重要是因为 DLQ 是 bus 的调试入口。出问题了？`kubectl exec` 进 sidecar，打开 BoltDB 文件，看最近 N 条。我们靠这个抓到过几个 bug，如果走"失败就丢"的路子根本看不到。

```go
func (d *DLQ) PurgeExpired() (int, error)
func (d *DLQ) Size() int
func (d *DLQ) List() ([]*DLQEntry, error)
```

**故意**没做从 DLQ 重放。死了就死了，我们要人的注意力，不要自动重试把真正的问题掩盖掉。

## 机制 5 — 环形缓冲 + 背压

剩下最后一个问题：channel sidecar 生产速度超过 runtime 消费能力怎么办？

朴素答案：无界队列。结果：OOM。

真实答案：有界环形缓冲 + 高低水位。

```go
func NewRingBuffer(size int, highWatermark, lowWatermark float64) *RingBuffer {
    // ... 默认 high=0.8, low=0.3 ...
}
```

缓冲填到 80% 时，bus 向上游发 `slow_down` 控制消息。channel sidecar 看到就停止从 Slack 拉新消息。缓冲降到 30% 时，bus 发 `resume`，sidecar 重新开始拉。

为什么要两个水位线？因为用一个会抖。就在阈值附近，每次 push 都翻转状态。两个水位之间留一个 gap 就有了滞后性。经典控制论，没多少 Go 代码。

`slow_down` / `resume` 消息走跟普通消息一样的线上格式：

```go
switch m.Type {
case TypeAck, TypeNack, TypeSlowDown, TypeResume,
     TypeShutdown, TypeRegister, TypeHeartbeat:
    return true
}
```

把控制流量当成另一种 `MessageType` 处理，channel sidecar 就不需要单独的控制通道。一条 TCP/UDS/WS 连接同时承载载荷和背压信号。更简单，故障模式更少。

## 优雅关闭

优雅关闭是它自己的雷区。SIGTERM 时 `cmd/ipcbus` 二进制跑一个本地 `shutdown()` 助手函数，做最基本的事：

```go
func shutdown(logger, router, wal, bridge, cancel) {
    router.SendShutdown()       // 告诉 sidecar 要走了
    time.Sleep(5 * time.Second) // 固定 grace window
    wal.Flush()                 // Flush WAL
    bridge.Close()              // 关 runtime bridge
    cancel()                    // 停 UDS server 和后台 ticker
}
```

就这么简单。不轮询、sidecar 提前断开也不会提前退出、也不关 DLQ（进程退出时 BoltDB 的 mmap 会被 flush，够用）。关闭时还是 `pending` 的 WAL 记录会在下次启动时重放——这就是 WAL 存在的意义。

库里还有一个更讲究的 `ShutdownOrchestrator`（在 [`internal/ipcbus/shutdown.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/shutdown.go)），接受 `drainTimeout` 参数，每 100 ms 轮询 `router.ConnectedCount()` 做真正的 wait-for-drain。当前二进制没有用它。不错的第一个 PR 切入点：把本地 helper 换成这个 orchestrator，把 sleep 换成真正的轮询。

## 故意没做的（也很重要）

- **跨 Pod 集群化**。Bus 刻意只管 Pod 内部。想跨 Pod，请用真正的 broker（NATS、Redis Streams）。限定在一个 Pod 保持了我们神志清醒。
- **跨 channel 的顺序保证**。单 channel 内有序，跨 channel 不承诺。大多数 agent 工作负载不在乎。
- **exactly-once**。at-least-once + 幂等消费者更简单且够用。runtime 负责按 `Message.ID` 去重。
- **protobuf**。JSON 比 protobuf 大约 2 倍，但调试容易 10 倍。按我们的吞吐（每 Pod 每秒几十条，不是百万），JSON 是对的选择。

## 测试

`ipcbus` 包大约在 80%+ 语句覆盖率。不明显的一点：大部分可靠性特性用 mock 很难测，因为它们是在测"失败模式"。所以我们写了很多测试是起真实的本地监听器（`net.Listen("tcp", "127.0.0.1:0")`、`net.Listen("unix", t.TempDir()+"/sock")`、`httptest.NewServer(...)`）然后端到端跑 bridge。

比如 SSE bridge 的测试起一个 `httptest` 服务器，同时处理 `GET /events`（SSE 流）和 `POST /messages`，验证连接、发送、接收都工作：

```go
func TestSSEBridge_SendReceive(t *testing.T) {
    srv, ready := sseEchoServer(t)
    defer srv.Close()

    bridge := NewSSEBridge(srv.URL)
    // ... connect, 等 SSE 流建立, send, receive ...
}
```

大概 70 个测试，`-race` 干净。对一个 sidecar 来说够用。

## 这套设计给我们换来了什么

channel sidecar 有了一个统一的契约。你写一个 Slack sidecar，它对每个 runtime 都工作。写一个 Discord sidecar，同理。runtime 作者按自己的技术栈挑协议，根本不用想持久化、重试、背压——bus 帮他们处理。

新协议的 runtime adapter 大约 50 行。channel sidecar SDK（[`sdk/channel/`](https://github.com/Prismer-AI/k8s4claw/tree/main/sdk/channel)）把帧化完全藏起来，你就 `client.Send(ctx, json.RawMessage(...))` 然后走人。

整个 `ipcbus` 包大约 2k 行 Go。想读一个文件了解全貌的话，[`router.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/ipcbus/router.go) 是五个机制交汇的地方。

## 下一步看什么

- [k8s4claw 仓库](https://github.com/Prismer-AI/k8s4claw) —— 想用
- [`internal/ipcbus/`](https://github.com/Prismer-AI/k8s4claw/tree/main/internal/ipcbus) —— 想读代码
- 相关综述：[k8s4claw：用一个 CRD 管理 K8s 上的 AI Agent 运行时](https://dev.to/willamhou/k8s4claw-a-kubernetes-operator-for-managing-ai-agent-runtimes-3anm)（或知乎版）

Apache-2.0 开源。欢迎提问和 PR。如果你做过类似的东西但选择了不同的方向，评论里讲讲为什么，我很想听。
