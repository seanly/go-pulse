# 0011 租约派发、ack 与租约回收

**里程碑** M3 · **依赖** 0003, 0010

## 目标

`pulsequeue`：按容量把 job 租给在线 agent，处理 ack，回收过期租约。从 certpulse `internal/scheduler` 里**切出通用部分**。

## 切分线

这是整个抽取里唯一有真正设计风险的一步。certpulse 的 `Scheduler` 把两件事混在一起了：

| 归框架（`pulsequeue`） | 留 certpulse |
|---|---|
| `DispatchOnce`、`quotaFor`、`pushJob`、`requeue` | `RunTask`、`planRun`、`expandTargets`（目标展开） |
| `ReapOnce`（租约回收） | `refreshTargetSummary`（业务反范式化） |
| `JobAck` | `RecordRuleSkip`（规则语义） |
| 派发唤醒与节流 | 告警钩子、证书指纹变更检测 |
| 重试预算（`attempt` / `max_attempts`） | `RetryRun` / `RetryJob` 的运维语义 |

判据：**框架管「一个 job 怎么安全地到达一个 agent 并回来」，应用管「该造哪些 job、结果意味着什么」**。

## 派发不含选择策略

`pulsequeue` 只回答「这个在线 agent 还能吃几个，给它」。「这个 subject 该给哪些 agent」由应用在入队时决定并写进 job 行的 `agent_id`。

框架给标签选择的原语（`pulse.LabelSelector`），不给规则语言。certpulse 的 `ruleengine` 有内外网判定、证书模式这类业务概念，不属于框架。

## 配额 = 有效并发 − 已持有

```
quota = effectiveConcurrency(agent) - leasedCount(agent)
```

配额 ≤ 0 就跳过这个 agent。硬租了只会换来一串 `busy` 拒绝，而每次拒绝都是一个往返加一次写。

没有服务端并发配置时，有效并发退化为 `DefaultBatch`——这是个猜测值,注释里要说明它是猜测。

## 每条路径都要让 job 落地

`pushJob` 的每个分支都必须把 job 放到某个确定状态。既没推出去也没置失败的租约 job 会一直隐身,直到 reaper 发现——对新租约来说那是一整个租约周期。

## 租约 SQL 是单条语句

这是存储实现里最容易写错的地方，`JobStore.Lease` 的文档必须写明：

- **postgres**：`WITH candidate AS (SELECT ... FOR UPDATE SKIP LOCKED) UPDATE ... RETURNING`
- **sqlite**：单写者语义下 SKIP LOCKED 无意义也不支持，靠 `UPDATE ... WHERE id IN (SELECT ...) RETURNING` 压成单条

**不能写成 SELECT 再 UPDATE**——调用方不在事务里时,那在任何方言下都是竞态。

## 两种「还回去」是不同的

certpulse 区分得很对，保留：

| | 计入 `attempt` | 含义 |
|---|---|---|
| `Fail` | 是 | 活干了但失败了 |
| `Requeue` | 否（单独计 `requeue_count`） | 根本没开始干（agent 满了、推送失败） |

满载的 agent 不该烧掉 job 的重试预算。但 `requeue_count` 也有上限，否则一个永久饱和的 agent 能让 job 无限打转。

`expired` 也是独立的终态，不并进 `failed`：一直超时意味着 agent 不答话了（agent 的问题），而 failed 意味着活跑了并且报告了什么。合并两者会让「这个 subject 到底能不能探测」从 job 表里看不出来。

## 报告驱动的派发唤醒

一个 job 完成就空出一个槽位。唤醒派发能把空槽立刻填上,而不是等下一个 tick。

但唤醒必须**合并**：一轮里几百个结果回来，不能变成几百次派发扫描。用容量 1 的 buffered channel 加非阻塞写——一堆报告合并成一次额外扫描。再加一个最小间隔节流。

## 验收标准

- [x] 按配额租约，配额 ≤ 0 时跳过，有测试
- [x] 推送失败的 job 被 requeue 且不烧 `attempt`，有测试
- [x] `busy` ack 释放 job 给别的 agent；`rejected` 不改派，有测试
- [x] 租约过期 → 可重试的回队、预算耗尽的转 `expired` 并交出去让应用合成结果
- [x] 同一个 job 在并发租约下不会被租两次，有测试（两个 goroutine 同时 Lease）
- [x] 唤醒合并：N 个报告不产生 N 次扫描，有测试
- [x] 重复上报同一个 job_id → `accepted=false`，有测试
