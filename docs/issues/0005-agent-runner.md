# 0005 Worker pool：并发、QPS、超时、drain

**里程碑** M1 · **依赖** 0004

## 目标

`pulseagent/runner.go`：job 从流上来，经过准入检查、ack、执行、回报。移植 certpulse `internal/agent/runner.go`。

## 范围

- 并发闸门（可热改上限）
- QPS 限速（`golang.org/x/time/rate`）
- 每 job 超时，取服务端请求值与本地上限的较小者
- `Executor` 按 `kind` 分发
- drain：停止取新活，等在跑的结束（30s 上限）

## 接收循环绝不阻塞

这是整个 agent 最重要的性质。慢 job 堵住消息读取，会连带堵住心跳，服务端就把 agent 判成离线了。

所以 `dispatch` 必须立刻返回：拿不到并发槽位就 ack `busy` 让服务端改派，**不排队**。certpulse 的注释说得对——「agent 现在跑不了的 job，交给别的 agent 比排在这个 agent 的积压后面更好」。

## Executor 注册表

```go
type Executor interface {
    Execute(ctx context.Context, job pulse.Job) (pulse.Result, error)
}

// Executors 把 kind 映射到执行器。空 kind 走 ""，单一用途的 agent
// 可以只注册它。
type Executors map[string]Executor
```

未注册的 kind → ack `unsupported_kind`，不是静默丢弃。certpulse 的「prober 为 nil 时回报 `prober_unavailable`，而不是假装拨测了」是同一个取向：骨架端到端跑通并说明情况。

## error 到 Result 的翻译

`Executor` 返回 `(Result, error)`：

- `error != nil` → 框架生成 `status=error`、`error_code=executor_error`、`error_detail=err.Error()`，并保留 Executor 填了的字段
- `error == nil` → 原样上报，应用完全控制状态（含 `skipped`）
- panic → 捕获、翻成 `status=error`、`error_code=executor_panic`，agent 不死

panic 恢复是新增的（certpulse 没有）。库的用户会写出 panic 的 Executor，一个 job 不该带走整个 agent。

## 热改限额

并发、QPS、超时都通过原子变量读写，配置下推（issue 0006）改它们不重启连接：

- 并发上限改小 → 不杀在跑的 job，只是之后少发槽位
- 超时改变 → 只影响新 job，已经在跑的保持它启动时的超时
- QPS → `rate.Limiter.SetLimit` 原地改

burst 等于 limit，不是 1——burst 1 会把并发闸门已经放行的 job 又串行化了。

## 验收标准

- [x] 并发上限生效，满槽位时 ack `busy` 而非阻塞，有测试
- [x] QPS 限速生效，有测试
- [x] 服务端给的超时大于本地上限时，取本地上限
- [x] 未注册 kind → ack `unsupported_kind`
- [x] Executor panic 被捕获并翻成 error result，agent 存活
- [x] drain 等在跑的 job 完成；超过上限则放弃并让服务端的租约过期接手
