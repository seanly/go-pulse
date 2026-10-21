# 0003 核心类型与存储端口

**里程碑** M0 · **依赖** 0001

## 目标

`pulse` 包：框架的词汇表。所有其他包都依赖它，它不依赖任何同级包。

## 范围

- `pulse/types.go`：`Job`、`Result`、`Agent`、`AgentConfig`、`BootstrapToken`、`LocalConfig`
- `pulse/ports.go`：`AgentStore`、`TokenStore`、`ConfigStore`、`JobStore`、`Registry`、`Sink`
- `pulse/protocol.go`：两端共享的常量（心跳间隔、staleness、状态值、ack 原因码）
- `pulse/errors.go`：`ErrNotFound`、`ErrConflict` 等端口契约里的哨兵错误
- `pulse/metrics.go`、`pulse/logging.go`：注入接口

## 端口拆成四个

```go
type AgentStore interface { ... }   // agent 行、心跳、离线扫描
type TokenStore interface { ... }   // bootstrap token 签发与 claim
type ConfigStore interface { ... }  // 下推配置的存取
type JobStore interface { ... }     // 入队、租约、完成、失败、回收
```

不合成一个大 `Store`：`pulsereg` 不需要 job 表，`pulsequeue` 不需要 token 表。下游也可能只接一半（比如只用 agent 侧 + 自己的派发逻辑）。

## 注册的原子性

注册要同时写 agent 行和 claim token，而 `used_by` 有指向 agent 的外键（逐语句检查，不是 commit 时检查），所以 agent 行必须先存在。certpulse 把两件事放进一个事务，失败则一起回滚。

端口不暴露泛型事务包装器（那会把 `database/sql` 的形状泄进接口），而是给一个成品方法：

```go
type TokenStore interface {
    // Enroll 在一个事务里写 agent 行并 claim token。token 无效时整个
    // 操作回滚，不留下半个 agent 行。
    Enroll(ctx context.Context, a *Agent, tokenHash string) (BootstrapToken, error)
    ...
}
```

实现怎么保证原子性是实现的事——SQL 用事务，内存实现用一把锁。

## 哨兵错误是契约的一部分

`pulsereg` 要区分「没这个 agent」和「数据库炸了」，所以 `ErrNotFound` 必须由端口定义而不是各实现自己造。同理 `ErrConflict`：租约被别人抢走、job 状态不允许该转换，都是「正常的竞态」而不是故障。

实现必须用 `fmt.Errorf("...: %w", pulse.ErrNotFound)` 包装，调用方用 `errors.Is` 判定。

## 状态与原因码

job 状态：`pending` / `leased` / `done` / `failed` / `expired` / `cancelled`。

result 状态：`ok` / `warning` / `error` / `skipped`。框架只用它做指标分类，不解释语义——`warning` 对证书是「快过期了」，对 CI 可能是「测试有 flaky」，都合理。

ack 原因码：`busy`（容量满，服务端应改派）、`rejected`（本地策略拒绝，改派也没用）、`unsupported_kind`（这个 agent 不会干这活）。certpulse 的 `invalid_target` 归入 `rejected`。

## 验收标准

- [x] `pulse` 包不 import 任何同级包（只许 stdlib + grpc/proto）
- [x] 每个端口方法的注释写清并发语义与哨兵错误
- [x] 心跳与 staleness 常量只在这里定义一次
- [x] `go vet ./pulse/...` 通过
