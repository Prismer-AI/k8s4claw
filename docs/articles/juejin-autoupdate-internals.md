---
title: "Kubernetes 工作负载自动更新：注解驱动的滚动 + 熔断器"
platform: 掘金
tags: Kubernetes, Go, 开源, 自动化运维
---

# Kubernetes 工作负载自动更新：注解驱动的滚动 + 熔断器

> 本文讲的是 [k8s4claw](https://github.com/Prismer-AI/k8s4claw) 里的自动更新控制器：定期查 OCI 仓库 → 按 semver 选高版本 → 翻一个注解 → 让主控制器去做滚动更新 → 如果不健康就回滚。整个控制器一个 Go 文件，约 470 行。

集群里有十个 agent Pod，每个跑不同的 runtime 镜像。每周二某个 runtime 发新版本。你打算手动 `kubectl set image` 改十个 StatefulSet 吗？真要出问题，你能确定是哪个版本搞挂了吗？

这篇是 [k8s4claw](https://github.com/Prismer-AI/k8s4claw) 里自动更新控制器的设计走读——不是 README，不是 API reference，是写给自己将来还要改这块代码的人看的。

## 问题的形状

打开自动更新的 `Claw` 长这样：

```yaml
spec:
  runtime: openclaw
  autoUpdate:
    enabled: true
    schedule: "0 3 * * *"           # 每天凌晨 3 点
    versionConstraint: ">=1.0.0,<2"
    healthTimeout: "10m"
    maxRollbacks: 3
```

控制器要做的事：

1. 按 cron 表达式定期醒来（不是"每 N 秒轮询"）。
2. 问 registry：`ghcr.io/prismer-ai/k8s4claw-openclaw` 有哪些 tag。
3. 过滤出落在约束区间内的 semver tag。
4. 选其中最高的、严格大于当前运行版本的、且不在"已失败列表"里的。
5. 应用——但不是直接 patch StatefulSet。
6. 在 `healthTimeout`（默认 10 分钟）内观察 ready 状态。
7. 如果 `sts.Status.UpdatedReplicas` 和 `sts.Status.ReadyReplicas` 都达到期望副本数：记录成功，归零回滚计数器。
8. 如果超时：清空 `target-image` 注解（让主控制器把 image 还原到 adapter 默认值），把这个版本加入"失败列表"，回滚计数 +1。
9. 连续 `maxRollbacks` 次失败：打开熔断器，停止尝试。后续版本检查不再应用新镜像，只发"有新版本但熔断已开"的事件/condition。

不显然的两个点：**状态存在哪儿**，**滚动是怎么发生的**。两者用的是同一个套路。

## 机制 1 — 注解驱动 in-flight 滚动

自动更新控制器在 reconcile 之间不持有任何内存状态。状态分两处放在 `Claw` 资源上：

- **注解** 描述正在进行的更新——目标镜像、当前阶段、开始时间。
- **`status.autoUpdate`** 存持久化簿记——当前版本、可用版本、回滚计数、熔断标志、失败版本列表、版本历史。

三个注解：

```go
const (
    annotationTargetImage = "claw.prismer.ai/target-image"
    annotationUpdatePhase = "claw.prismer.ai/update-phase"
    annotationUpdateStart = "claw.prismer.ai/update-started"
)
```

- `target-image` — 想运行的完整镜像引用（`ghcr.io/.../openclaw:1.2.0`）。更新成功后这个注解**保留**。
- `update-phase` — 目前只有 `HealthCheck` 或不存在。不存在 = 空闲。其他任何值都走空闲路径。
- `update-started` — 设置 phase 注解时的 RFC3339 时间戳。健康检查计时器用。

Reconcile 在 phase 上分二叉：

```go
phase := claw.Annotations[annotationUpdatePhase]
if phase == "HealthCheck" {
    return r.reconcileHealthCheck(ctx, &claw)
}
// 否则：空闲——看是不是该轮询版本了
```

这意味着控制器**无状态、幂等**。operator Pod 中途重启，下一次 reconcile 从 etcd 读回注解，从断点继续。没有 `map[types.NamespacedName]updateState` 要重建，没有 in-flight 工作的选主问题。Kubernetes 是数据库，控制器是当前状态的纯函数。

附带好处：`kubectl describe claw foo` 直接能看到 in-flight 更新。不需要 trace，不需要 grep 控制器日志。状态在资源上。

## 机制 2 — 滚动只是一个注解

写这个控制器时让我意外的一点：自动更新逻辑**不 patch StatefulSet**，**不碰 Pod**。它做的就是：

```go
targetImage := baseImage + ":" + newVersion
claw.Annotations[annotationTargetImage] = targetImage
claw.Annotations[annotationUpdatePhase] = "HealthCheck"
claw.Annotations[annotationUpdateStart] = now.Format(time.RFC3339)
r.Update(ctx, &claw)
```

就这些。这就是"应用一个新版本"代码路径的全部。

滚动之所以发生，是因为**主**控制器 `ClawReconciler` 监听同一个 `Claw` 资源，每次 reconcile 都重建 pod template。重建时它会读这个注解：

```go
// claw_controller.go
podTemplate := adapter.PodTemplate(claw)

// Auto-update: override runtime image if target-image annotation is set.
if targetImage := claw.Annotations["claw.prismer.ai/target-image"]; targetImage != "" {
    for i := range podTemplate.Spec.Containers {
        if podTemplate.Spec.Containers[i].Name == "runtime" {
            podTemplate.Spec.Containers[i].Image = targetImage
            break
        }
    }
}
```

所以自动更新控制器纯粹是个**信号源**。它说"我希望这个镜像在跑"。主控制器负责把这件事翻译成 StatefulSet 更新→滚动 Pod 替换。然后自动更新控制器通过 `sts.Status.UpdatedReplicas` 和 `sts.Status.ReadyReplicas` 观察结果。

这个分离重要在于：

1. **回滚基本就是删注解。** 回滚时 `delete(claw.Annotations, annotationTargetImage)`，主控制器下一次 reconcile 自然回到 adapter 的默认镜像。StatefulSet 逻辑里没有"回滚分支"。（`update-phase` 和 `update-started` 注解也会一并清理。）
2. **手动镜像覆盖照样能用。** 如果有人手动设了 `target-image` 做 hotfix，主控制器会把它当成 pod template image 用。自动更新控制器在判断"是否要提议新版本"时是和 `status.CurrentVersion` 比较（不是和注解比较），所以手动覆盖不会让控制器错认"当前"。
3. **完全删掉自动更新控制器也不会出问题。** 注解可能会陈旧，但集群不会塌。

如果你在写一个新控制器，发现自己直接在改子资源，问问能不能改成在父 CR 上改注解，让现有 reconciler 完成动作。基本上更干净。

## 机制 3 — semver 解析

选版本的逻辑在 [`internal/registry/resolver.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/registry/resolver.go)：

```go
func ResolveBestVersion(tags []string, constraint, current string, failedVersions []string) (string, bool) {
    c, err := semver.NewConstraint(constraint)
    if err != nil {
        return "", false
    }

    var currentVer *semver.Version
    if current != "" {
        currentVer, _ = semver.NewVersion(current)
    }

    failedSet := make(map[string]bool, len(failedVersions))
    for _, f := range failedVersions {
        failedSet[f] = true
    }

    var best *semver.Version
    for _, tag := range tags {
        v, err := semver.NewVersion(tag)
        if err != nil {
            continue // skip non-semver tags like "latest", "sha-abc"
        }
        if !c.Check(v) {
            continue
        }
        if failedSet[v.Original()] {
            continue
        }
        if currentVer != nil && !v.GreaterThan(currentVer) {
            continue
        }
        if best == nil || v.GreaterThan(best) {
            best = v
        }
    }

    if best == nil {
        return "", false
    }
    return best.Original(), true
}
```

三个细节：

- **非 semver tag 静默丢弃。** `latest`、`sha-abc1234`、`nightly` 全部 `semver.NewVersion()` 失败，被跳过。这是自动更新的正确默认行为：和版本约束没法比的东西，本来就不该被自动滚进生产。
- **`failedVersions` 在约束检查之后被检查，按精确原始 tag 字符串匹配。** 回滚过的版本被记到 `Status.AutoUpdate.FailedVersions`，自动选择时会被排除。匹配是 `v.Original()`，所以 `"1.2.0"` 和 `"v1.2.0"` 是两个不同的字符串——约束检查是 semver 语义的，但失败版本过滤不是。要让自动选择重新尝试一个失败版本，得手动从 status 里清掉；也可以走手动 annotations 路径强制滚一次（见后面熔断器一节）。这样设计是有意保守——v1.2.0 把你的 Pod 弄挂过一次，下一个凌晨 3 点的 cron tick 不会让它变好。
- **`!v.GreaterThan(currentVer)` 排除等于。** 每个 cron tick 都重装同版本会很吵。

控制器对 digest-pin 的镜像也有早退出分支：

```go
currentImage := claw.Annotations[annotationTargetImage]
if currentImage != "" && registry.IsDigestPinned(currentImage) {
    logger.Info("skipping auto-update: image is digest-pinned", "image", currentImage)
    return r.requeueAtNextCron(spec), nil
}
```

注意它检查的是 `target-image` **注解**，不是实际跑的镜像。`IsDigestPinned` 就是 `strings.Contains(image, "@sha256:")`。如果你把 `target-image` 设成 digest 引用（手动或之前的覆盖），控制器就不再按 cron 动这个 Claw。注解不存在时跳过这个检查，正常进入版本轮询。

## 机制 4 — 健康验证

注解设好之后，控制器每 15 秒重排队一次，盯 ready 状态：

```go
desiredReplicas := int32(1)
if sts.Spec.Replicas != nil {
    desiredReplicas = *sts.Spec.Replicas
}
if sts.Status.UpdatedReplicas >= desiredReplicas &&
   sts.Status.ReadyReplicas >= desiredReplicas {
    // 健康检查通过
}
```

两个条件，缺一不可：

- `UpdatedReplicas` — 用**新**模板跑的 Pod，不是老的。少了这个检查，老 Pod 还在 ready 时就会"成功"——但其实滚动还没开始。
- `ReadyReplicas` — 通过 readiness probe 的 Pod。

两个都在 `healthTimeout`（默认 10 分钟）内达成 → 记录成功：归零回滚计数器、归零熔断、追加 `Healthy` 版本历史项、清掉 `update-phase` 和 `update-started` 注解。这里**特意保留** `target-image`——它是主控制器用来覆盖 runtime 容器镜像的信号，清掉它会让运行中的 Pod 在下一次 reconcile 静默回到 adapter 默认镜像。

如果定时器先到：

```go
if r.clock().Since(startedAt) > healthTimeout {
    return r.rollback(ctx, claw, "health check timed out")
}
```

另外两种回滚触发：StatefulSet 在超时后还找不到（资源被删了），或 `update-started` 注解格式坏掉（这是字符串，得防御性处理）。

15 秒是轮询间隔，不是 deadline。真正的 deadline 是从 spec 解析的 `healthTimeout`。如果你在升一个启动要 8 分钟的重型 runtime，把 `healthTimeout` 设成 `15m`，控制器会等够这个时间。

## 机制 5 — 熔断器

回滚一次是小事故，连续回滚三次就是系统在告诉你停手。

```go
maxRollbacks := defaultMaxRollbacks  // 3
if spec.MaxRollbacks > 0 {
    maxRollbacks = spec.MaxRollbacks
}
if status.RollbackCount >= maxRollbacks {
    status.CircuitOpen = true
    SetAutoUpdateCircuit(claw.Namespace, claw.Name, true)
    r.Recorder.Event(claw, corev1.EventTypeWarning, EventAutoUpdateCircuitOpen,
        fmt.Sprintf("Circuit breaker opened after %d rollbacks", status.RollbackCount))
}
```

熔断打开后，主 Reconcile 路径仍然会发现新版本，但只发"版本 X 可用，但熔断已开"的事件，不再应用。用户在 `kubectl describe claw foo` 看到这个，自己决定要不要排查或人工覆盖。

恢复路径**故意**直白：**控制器不自动恢复熔断**。没有"等 24 小时再试"的定时器，没有指数退避，没有专门的试投部署。门控检查是 `if status.CircuitOpen`——它不看 `RollbackCount`。所以恢复路径有两条：

1. 人工 patch `status.autoUpdate.circuitOpen` 为 `false`（通常顺手把 `rollbackCount` 也改成 0），下一次 cron tick 恢复正常版本轮询。
2. 人工绕道：手动设全三个注解（`target-image` 指向已知好的镜像、`update-phase=HealthCheck`、`update-started` 是新鲜的 RFC3339 时间戳）。phase 检查在熔断检查之前发生，下一次 reconcile 直接进 `reconcileHealthCheck`，滚动成功后自动归零 `RollbackCount` 和 `CircuitOpen`。（`FailedVersions` 不动，所以那些失败版本仍不会被自动选中。）少了时间戳或 `target-image` 指向起不来的东西，会立刻回滚——所以手动路径必须三件齐全。

设计理由：连续三个坏版本通常意味着控制器视野外有别的问题（上游镜像坏、probe 写错、集群网络故障）。自动恢复只会在新的 schedule 上重新发现同样的破状态，多烧几次滚动。我们宁愿叫人。

如果你想加"先静置再重试"模式，自然的位置是在第 N 次"轮询无新版本"之后清掉 `CircuitOpen`——也就是一段稳定期。这是个合理的 PR。

## 机制 6 — 版本历史（带上限）

每次成功更新和每次回滚都会追加一条 `Status.AutoUpdate.VersionHistory`：

```go
status.VersionHistory = append(status.VersionHistory, clawv1alpha1.VersionHistoryEntry{
    Version:   version,
    AppliedAt: metav1.Now(),
    Status:    clawv1alpha1.VersionHistoryHealthy,  // 或 VersionHistoryRolledBack
})
trimVersionHistory(status)
```

`trimVersionHistory` 存在的理由是 etcd 对象有大小限制——日更新两年的 `Claw` 能攒 700+ 条历史：

```go
const maxVersionHistory = 50

func trimVersionHistory(status *clawv1alpha1.AutoUpdateStatus) {
    if len(status.VersionHistory) > maxVersionHistory {
        status.VersionHistory = status.VersionHistory[len(status.VersionHistory)-maxVersionHistory:]
    }
}
```

50 条够 debug 最近几个月的活动。要做长期审计，把控制器事件抓到你的可观测系统。Status 字段不是审计日志。

## Update 与 Status.Update 的两步舞

注解放在资源的 `metadata` 下。Status 字段在 `.status` 下。Kubernetes 通过两个不同的 subresource 写：

- `r.Update(ctx, claw)` — 写 `metadata` 和 `spec`。bump `resourceVersion`。
- `r.Status().Update(ctx, claw)` — 写 `.status`。也 bump `resourceVersion`。

一次 reconcile 两个都要写——比如"开始更新"路径——in-memory 的 `claw` 对象在两次调用之间会陈旧。控制器中间显式 re-fetch：

```go
// 先写注解，然后 re-fetch + merge status
if err := r.Update(ctx, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to set target-image annotation: %w", err)
}
// Re-fetch 拿新的 resourceVersion 再写 status
if err := r.Get(ctx, req.NamespacedName, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to re-fetch after annotation update: %w", err)
}
mergeAutoUpdateStatus(&claw, status)
for _, c := range pendingConditions {
    apimeta.SetStatusCondition(&claw.Status.Conditions, c)
}
if err := r.Status().Update(ctx, &claw); err != nil {
    return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
}
```

re-fetch 是为了拿到上一次写入后的新 `resourceVersion`，否则 `Status().Update` 会和我们刚才的写冲突。在任何不平凡的 reconcile 频率下你都会看到 409。

`mergeAutoUpdateStatus` 是另一半。它把本地跟踪的 status 字段一个一个拷进刚 fetch 出的对象里，而不是把 `claw.Status.AutoUpdate` 整个指针换掉。逐字段拷贝是保守做法：未来如果 `AutoUpdateStatus` 加了新字段而我们没在本地跟踪，整体替换会把它静默清零。merge 风格让控制器对 auto-update 子对象的写是增量的。

## 可测性：Clock 和 TagLister

两个接口，都为测试服务：

```go
type TagLister interface {
    ListTags(ctx context.Context, image string) ([]string, error)
}

type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
}
```

`TagLister` 让单测注入 `[]string{"1.0.0", "1.1.0", "2.0.0-rc1"}` 而不是真的请求 GHCR。`Clock` 让测试推进时间而不用 `time.Sleep`。两者都各有一行的生产实现和一行的 fake 实现。

manager 装配时这样接：

```go
// cmd/operator/main.go
registryClient := clawregistry.NewRegistryClient()
&controller.AutoUpdateReconciler{
    Client:    mgr.GetClient(),
    Scheme:    mgr.GetScheme(),
    Recorder:  mgr.GetEventRecorderFor("autoupdate-controller"),
    TagLister: registryClient,
    // Clock 不显式设；clock() 走 realClock{} 兜底
}
```

测试里两个字段都换 fake：

```go
cl := fake.NewClientBuilder().
    WithScheme(scheme).
    WithObjects(claw).
    WithStatusSubresource(claw).
    Build()
r := &AutoUpdateReconciler{
    Client:    cl,
    Scheme:    scheme,
    Recorder:  record.NewFakeRecorder(10),
    TagLister: &testTagLister{tags: []string{"1.0.0", "1.1.0"}},
    Clock:     &testClock{now: time.Now()},
}
```

reconcile 路径的单测用 `controller-runtime/pkg/client/fake`——没有 envtest API server，没有 kube-apiserver 进程，就一个内存 client 配 typed scheme。建一个 `Claw`，跑一次 `Reconcile`，对注解和 `Status.AutoUpdate` 断言。没有真的 registry 调用，没有真定时器，不 flaky。每个测试不到一秒。

如果你在 reconciler 里直接调 `time.Now()` 或外部 API，停下来先定义 interface。未来的你写测试时会感激现在的你。

## 我们故意没做的

- **预飞镜像探测。** 我们不会 pull 新镜像、在节点上 `docker run` 试一下再翻 StatefulSet。那需要重得多的依赖（DaemonSet？特权容器？），而 StatefulSet 滚动本身就是某种探测——readiness 检查直接在生产跑。
- **金丝雀部署。** 先滚一个 Pod 看看再滚剩下的。我们大多数 agent workload `replicas=1`，没什么可金丝雀。多副本部署确实值得做——现有状态机可以在 idle 和 `HealthCheck` 之间加一个 `Canary` 阶段。
- **registry webhook 推送。** 用 push 代替 poll。运维上更简单但会让 registry 反向依赖集群，大多数集群不要这种依赖。cron poll 在运维简单度上赢。
- **跨命名空间协调。** 同一个镜像有十个 `Claw` 用，坏版本来了它们各自独立回滚。我们考虑过用一个共享 `ClawImageGroup` 资源把它们绑起来，最后觉得复杂度不值。熔断器加失败版本列表已经够用：每个 `Claw` 自己学到痛。
- **镜像签名校验。** Sigstore / cosign 集成会接在 `IsDigestPinned` 那个层级——校验通过再设 `target-image`。我们没做是因为现在服务的项目还没到那一步，但对安全敏感的部署是明显的下一步。

## 测试

单测分散在三个文件：

- [`internal/controller/autoupdate_reconcile_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_reconcile_test.go) — reconcile 路径主集：发起更新、跳过 digest-pin、健康检查成功、超时回滚、连续回滚开熔断、StatefulSet 找不到、`update-started` 损坏立即回滚、自定义 `healthTimeout`、调度未到时的 requeue。
- [`internal/controller/autoupdate_controller_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_controller_test.go) — 混合：helper 覆盖（`extractVersionFromImage`、`trimVersionHistory`、`containsString`、cron 时机算法、`clock()` 里 `realClock` fallback），加上一小批 reconcile 测试（disabled、无新版本、not-found、熔断已开等路径）。
- [`internal/controller/autoupdate/autoupdate_controller_test.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate/autoupdate_controller_test.go) — 一个老的并行测试套件，仍然跑同一份控制器代码。

reconcile 路径测试预先放好 `Claw`（必要时再放一个 readiness 状态确定的 `StatefulSet`），跑一次 `Reconcile`，对注解或 `Status.AutoUpdate` 断言。大多数测试不到 50 行。fake clock + fake tag lister 让时序确定，这是测试不 flaky 的主要原因。

## 这套换来了什么

一个约 470 行的控制器，能做 cron 触发、semver 过滤、健康验证、自动回滚的镜像更新，带熔断器和版本历史。所有 in-flight 状态都在 `Claw` 资源上（注解管阶段、`.status` 管持久簿记），控制器在重启时没有内存状态会丢。支持的 runtime 类型由一个小的 `ImageForRuntime(string) string` 帮助函数映射到 base OCI 镜像——加新 runtime 就是那个 switch 加一个 case，不是改控制器。没在映射里的 runtime 会被自动更新静默跳过（目前 `hermesrs` 和 `k8sops` 就属于这种——它们没有公开 OCI release 节奏）。控制器其余部分是纯 semver tag 在工作。

我会指给一个 K8s 控制器初学者看的、这份代码里的关键洞见是：注解驱动的分离——*控制器不做滚动，它请求滚动*。理解了这点，很多 K8s 控制器都会变小。

## 接下来看哪里

- [k8s4claw repo](https://github.com/Prismer-AI/k8s4claw) 如果想用整个 operator
- [`autoupdate_controller.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/controller/autoupdate_controller.go) 一个文件看完控制器
- [`registry/resolver.go`](https://github.com/Prismer-AI/k8s4claw/blob/main/internal/registry/resolver.go) 看版本选择
- [IPC bus 深度文](https://dev.to/willamhou/building-an-ipc-bus-for-kubernetes-sidecars-wal-dlq-and-ring-buffer-backpressure-4b27) — 本系列第二篇，讲 channel sidecar 怎么和 runtime 通信

Apache-2.0。如果你写过带金丝雀或签名校验的自动更新器，我真的想看你的代码。评论留个链接。
