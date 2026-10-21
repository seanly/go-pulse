# 0006 配置下推与生效回显

**里程碑** M1 · **依赖** 0004

## 目标

服务端下推配置、agent 原子生效、心跳回显已生效版本。移植 certpulse 的 `applyConfig` / `applyPushed` / `validatePushed`。

这是 certpulse 里设计得最好的一块，几条语义都值得原样搬过来并写进框架文档。

## 四条保留的语义

**1. 未设置 = 无意见**

零值字段意味着「服务端对此无意见，用你本地的值」。所以服务端每次发全量配置，一个被清除的 override 以 0 到达，agent 回落到本地值——不需要重连，也不需要「清除」这个单独的动作。

**2. 全量校验，原子生效**

一个字段不合法就丢掉整条消息，`appliedConfig` 保持原值。控制台因此停在「等待生效」，运维能看出有问题。半套配置生效比旧配置继续跑更难排查。

**3. 校验在两端都做**

服务端在 PUT 时校验范围，agent 收到时再校验一遍。这不是冗余——对面可能是更老、更新或者有 bug 的服务端。

**4. 重连时重新应用已知配置**

上一个 session 下推的配置活过 session。服务端会在 connect 时重推，但先应用已知的能避免重连后有一轮跑在旧值上。

## RELOAD 的顺序坑

RELOAD 是「重读本地文件，然后把下推的 override 重新叠上去」。少了第二步，agent 会悄悄回落到本地 qps，而服务端仍认为自己的值生效中、`config_version` 也还对得上——这个 bug 很难查。

certpulse 的注释专门警告了这点。框架里保留这个行为和注释。

## 日志级别下推

certpulse 调 `logging.SetLevel`（包级全局）。框架改成注入：

```go
type LevelSetter interface {
    SetLevel(level string) error
}
```

`Options.LevelSetter` 为 nil 时，级别下推是 no-op 并打一行日志说明——不是报错。一个不想让服务端改自己日志级别的部署是合理的。

## payload：应用自己的配置

`AgentConfig.payload` 是不透明字节，框架搬运但不解读。`Options.OnConfigPayload func([]byte) error` 让应用处理自己的部分；它返回 error 时，整条配置按语义 2 被丢弃。

`pulseguard`（issue 0007）就是这个钩子的第一个用户：deny list 走 payload，不占信封字段。

## 验收标准

- [x] 零值字段回落本地值，有测试（覆盖「推了又清除」的来回）
- [x] 非法字段丢弃整条消息且 `appliedConfig` 不变，有测试
- [x] 心跳携带已生效版本；重连不会让版本看起来回滚到 0
- [x] RELOAD 后下推的 override 仍然生效，有测试
- [x] `LevelSetter` 为 nil 时级别下推不报错
- [x] `OnConfigPayload` 返回 error 时整条配置被丢弃
