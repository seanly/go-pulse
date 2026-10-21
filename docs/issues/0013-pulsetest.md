# 0013 测试工具：内存 store 与假 stream

**里程碑** M4 · **依赖** 0003

## 目标

`pulsetest`：下游写测试时最先需要的东西。

## 为什么这个包不能省

certpulse 的 `agent_test.go`、`agentreg_test.go`、`scheduler_test.go` 加起来 2000 多行,里面相当一部分是假件——假 API、假流、内存存储。下游接入框架时会立刻需要同样的东西，如果不给，每个项目都要重写一遍，而且会写得不一致（比如忘记模拟「流断开」这种路径）。

## 范围

```go
// Store 是四个端口的内存实现，并发安全。
func NewStore(opts ...StoreOption) *Store

// API 是 pulseagent.API 的假实现：可以注入 job、断言收到的结果、
// 模拟断线与认证失败。
func NewAPI() *API

// Registry 是 pulse.Registry 的假实现，记录推给每个 agent 的消息。
func NewRegistry() *Registry

// Clock 是可手动推进的时钟，给租约过期和 staleness 测试用。
func NewClock(start time.Time) *Clock
```

## 假件要能模拟坏路径

好路径的假件没什么价值。这些必须能模拟：

- 流在任意时刻断开（`API.BreakStream(err)`）
- 认证失败（`API.RejectCredential()`），测重新注册
- 发送队列满
- 存储返回 `ErrNotFound` / `ErrConflict`（`Store.FailNext(err)`）
- 租约竞态（`Store` 的 Lease 可以注入一个「另一个进程抢先」的钩子）

certpulse 的 `integration_test.go` 用这类注入测出了实际的 bug，这个能力要保留。

## Clock 而不是 sleep

租约过期、staleness、退避全部依赖时间。测试里不能真睡——45 秒的 staleness 测试睡不起。

所有框架组件的 `Options` 都有 `Now func() time.Time`（certpulse 已经是这个模式），`pulsetest.Clock` 提供可推进的实现。

## goleak

框架起了不少 goroutine（心跳、sender、receiver、各种 loop）。`pulsetest` 导出一个 `VerifyNoLeaks(t)` 包装 `go.uber.org/goleak`，下游能一行加上这个检查。

certpulse 已经依赖 goleak，所以这个依赖是已经验证过的。但它只进 `pulsetest`,不进核心包的依赖图。

## 验收标准

- [x] 内存 Store 通过与 `sqlstore` 相同的端口契约测试（共享一套 testsuite）
- [x] 假 API 能模拟断线、认证失败、队列满
- [x] Clock 可手动推进，租约与 staleness 测试不 sleep
- [x] `VerifyNoLeaks` 可用
- [x] `pulsetest` 的依赖（goleak）不进核心包依赖图
