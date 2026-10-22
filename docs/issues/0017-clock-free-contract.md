# 0017 契约套件不再需要可控时钟

**里程碑** M4 · **依赖** 0012, 0013

## 来历

这个 issue 也不在原计划里。certpulse 接入（issue 0015）时，契约套件里 4 个用例被 skip 掉了：

```
SKIP HeartbeatAndStaleness
SKIP EnrollRejectsUnknownUsedExpiredIdentically
SKIP ReclaimRequeuesAndExpires
SKIP PurgeFinished
```

原因是 `Stores.Clock` 为 nil —— certpulse 的 store 通过一个包级函数 `now()` 读墙上时钟，没法注入。套件原本靠 `Clock.Advance()` 让租约过期、让心跳变 stale，拿不到可控时钟就只能跳过。

**4 个 skip 里有 `ReclaimRequeuesAndExpires`** —— 租约过期回收，整个队列里最该被契约覆盖的路径之一。「跳过」在这里等于「没验证」。

## 为什么不是让实现注入时钟

把 `now()` 改成可注入，意味着在 store 包里放一个包级可变变量。那正是框架在 §7（去全局化）里专门从 certpulse 移除的东西——为了让测试能跑而把它加回下游，方向是反的。

而且这个要求会传染：每个想跑契约的下游都得先改造自己的时钟。一个「只有重构过时钟的项目才能验证」的契约套件，用的人会少很多。

## 做法：把截止时间拨到过去，而不是把时间拨到未来

每一处「等到某个时刻过去」都可以反过来问：

| 原来 | 现在 |
|---|---|
| `Clock.Advance(2*time.Minute)` 让租约过期 | `LeaseOne(id, -time.Minute)` —— 租约期限本来就在过去 |
| `Clock.Advance()` 让心跳变 stale | `MarkStaleAgentsOffline(-time.Hour)` —— 截止点在未来，所有心跳都算过期 |
| `Clock.Advance(time.Hour)` 再按 30 分钟前清理 | `PurgeFinished(now + time.Hour)` —— 截止点在未来 |
| `Clock.Advance()` 让 token 过期 | 签发时就把 `expires_at` 设在过去 |

certpulse 自己的 `TestReclaimExpiredLeases` 本来就是这么写的（`LeaseJobs(..., -time.Minute)`），这个技巧是从它那里学来的。

## 负数是契约的一部分，不是测试技巧

这点必须写进端口文档，否则一个实现完全可以合理地把负数 clamp 成 0——然后测试静默变绿，什么都没验证。

所以 `pulse.JobStore.LeaseOne` 和 `pulse.AgentStore.MarkStaleAgentsOffline` 的文档里都写明了：**参数按原样使用，包括零和负数，实现不得 clamp**。

生产里不会传负数。它存在是因为「租约期限已经过去」就是过期租约的定义，而测试需要一个不依赖时钟的说法。

## 结果

- 契约套件的 4 个 skip 消失，certpulse 的适配器 **33/33 全过**
- `requireClock` 删掉，`Stores.Clock` 降级为可选（只用于读——套件的时间戳要和 store 写的时间戳在同一条时间线上）
- `porttest.Clock` 接口不再要求 `Advance`
- 任何「用包级函数读墙上时钟」的 store——也就是大多数 store——都能跑完整个契约

## 验收标准

- [x] 四个原本 skip 的用例在两个参考实现上都通过
- [x] 同样四个用例在 certpulse 的适配器上通过（它没有可控时钟）
- [x] `LeaseOne` 与 `MarkStaleAgentsOffline` 的文档写明负数行为，禁止 clamp
- [x] `requireClock` 删除，套件里没有任何 skip 路径
- [x] `Stores.Clock` 为 nil 时整个契约可跑
