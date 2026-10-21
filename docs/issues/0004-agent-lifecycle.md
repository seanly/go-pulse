# 0004 Agent 生命周期：注册、重连、心跳

**里程碑** M1 · **依赖** 0002, 0003

## 目标

`pulseagent` 的主循环。移植 certpulse `internal/agent/agent.go`，去掉对 config 包、logging 全局和 version 全局的依赖。

## 范围

- `pulseagent/agent.go`：`New`、`Run`、`session`
- `pulseagent/api.go`：`API` / `Stream` 接口与 gRPC 适配
- `pulseagent/credential.go`：凭据落盘（0600、临时文件 + rename）
- `pulseagent/backoff.go`：指数退避 + ±20% 抖动

## 主循环

```
读 credential_file
  ├─ 有 → Connect
  └─ 无 → Register(bootstrap_token) → 落盘 0600 → Connect
Connect
  ├─ 心跳协程：15s 一次，回显已生效的 config version
  ├─ 接收协程：job → worker pool（绝不阻塞）；command；config
  └─ 断线 → 退避重连（1s 起，60s 上限，±20% 抖动）
凭据被拒（Unauthenticated）
  ├─ 有 bootstrap_token → 删除凭据文件 → 重新注册
  └─ 无 → 退出并说明原因（不是静默重试）
```

## 移植时要改的

**配置来源**。certpulse 的 RELOAD 直接调 `config.LoadAgent(a.cfgFile)`。框架不解析 TOML：

```go
type Options struct {
    // Config 是启动时的配置。
    Config RuntimeConfig
    // ReloadConfig 在收到 RELOAD 命令时被调用，返回新配置。nil 则
    // RELOAD 只重新应用已下推的 override，不碰本地配置。
    ReloadConfig func() (RuntimeConfig, error)
    ...
}
```

certpulse 传 `func() (pulse.RuntimeConfig, error) { return adapt(config.LoadAgent(path)) }`。

**身份与连通性不可热改**。certpulse 在 reload 后把 `Name`/`ServerAddr`/`BootstrapToken`/`CredentialFile` 强制覆盖回原值——运行中的 agent 不能改身份。这条保留。

**单发送者**。grpc-go 的流只允许一个发送 goroutine，而 agent 从心跳、ack、shutdown 三处发送。certpulse 用一个 sender goroutine 加 `sendCh`，且 `sendCh` 永不 close（正在发送的心跳会 panic on closed channel），用 `senderDone` 退休 sender。这个结构原样保留,注释也保留——它是踩过坑的。

## 必须保留的两个细节

1. **连接后立刻发一次心跳**，不等第一个 tick。服务端自己也会在 Connect 时记一次，但显式心跳让两边不会对「谁把 agent 翻成在线」有分歧。
2. **`appliedConfig` 挂在 Agent 上而不是 session 上**。否则重连看起来像配置回滚到版本 0。

## 验收标准

- [x] 首次启动能注册并落盘，重启复用凭据
- [x] 凭据被吊销后能重新注册；无 bootstrap_token 则报错退出
- [x] 退避有指数增长与抖动，有测试（固定 rand 源）
- [x] SIGTERM 后停止取新 job、等运行中的跑完、再断连
- [x] `goleak` 验证 session 结束后无泄漏 goroutine
