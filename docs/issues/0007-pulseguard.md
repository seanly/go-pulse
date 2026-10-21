# 0007 可选的目标准入策略

**里程碑** M1 · **依赖** 0005

## 目标

`pulseagent/pulseguard`：按 CIDR 与域名 glob 拒绝目标。移植 certpulse `internal/agent/guard.go`。

单独成包是因为它只对「向外发起网络请求」的 agent 有意义。CI 构建 agent 不需要它，不该被迫编译进去。

## 为什么框架要管这个

certpulse 的注释说清了理由：**服务端规则配错时，这是最后一道防线**。内网 agent 尤其需要——一个配置错误不该让它去扫整个内网网段。

这个论证对任何「agent 按服务端指令访问网络」的系统都成立，所以它属于框架而不是应用。

## deny list 只增不减

下推的 deny 条目与本地的取**并集**，本地条目永不被移除。

这是单向的，故意的：本地 deny list 存在的意义就是防服务端出错，如果一次下推能放宽它，这层保护就成了装饰。控制台因此应该把下推的条目叫「额外禁止」而不是「deny list」。

`allow_loopback` 同理——它是机器主人的决定，所以**根本不在下推消息里**。

## 解析后检查

地址类 deny 规则只能在 DNS 解析后执行，而 runner 看不到解析结果。所以 `Guard` 暴露两个方法：

```go
// CheckTarget 在派发前按域名检查。
func (g *Guard) CheckTarget(host string, port int) error
// CheckIPs 在 Executor 解析出地址后检查。Executor 必须自己调它。
func (g *Guard) CheckIPs(ips []net.IP) error
```

`CheckIPs` 由 Executor 负责调用,框架调不了——这点在 `Executor` 接口的文档里写明。

## 配置走 payload

Guard 的配置编码进 `AgentConfig.payload`：

```go
// Policy 是 pulseguard 的下推配置，JSON 编码进 AgentConfig.payload 的
// "pulseguard" 键下,与应用自己的配置共存。
type Policy struct {
    DenyCIDRs   []string `json:"deny_cidrs,omitempty"`
    DenyDomains []string `json:"deny_domains,omitempty"`
}
```

payload 顶层是一个 JSON object，按键分区，所以 pulseguard 和应用配置互不干扰。`pulseguard.Hook(local Policy)` 返回一个可以直接给 `Options.OnConfigPayload` 的函数。

## 验收标准

- [x] CIDR 与域名 glob 命中 → 拒绝，原因码 `rejected`
- [x] 环回地址默认拒绝，`AllowLoopback` 打开后放行，有测试
- [x] 非法端口拒绝
- [x] 下推的 deny 条目与本地取并集，本地条目不被移除，有测试
- [x] 下推里不存在放宽 `allow_loopback` 的途径（类型上就没有这个字段）
- [x] `pulseguard` 只被 `pulseagent` 可选引用，核心路径不依赖它
