# 0016 派发时解析载荷

**里程碑** M4 · **依赖** 0011

## 来历

这个 issue 不在原计划里。它是 certpulse 接入（issue 0015）第一天就撞上的墙：certpulse 的 job 行**没有载荷**。

它在派发时从 target 行读 fqdn/port/sni，所以一个入队之后被编辑过的 target 仍然按当前设置拨测。框架原先只能搬运入队时写进去的载荷——对 certpulse 来说，那意味着改一个 target 的端口就得把它所有排队的 job 重新入队一遍。

这不是 certpulse 的特殊癖好。任何「工作由可变状态描述」的系统都是这样：CI 要用当前的 commit，部署要用当前的 release。入队时的载荷是个快照，而快照往往不是想要的东西。

## 设计

`Options.ResolvePayload` 在每个 job 推送前被调用一次，决定它携带什么——或者它根本不该被推送：

```go
type PayloadResolver func(ctx context.Context, j pulse.Job) Decision

type Decision struct {
    Action  Action        // 零值是 ActionPush
    Payload []byte        // 非 nil 时替换载荷
    Timeout time.Duration // 非零时覆盖超时
    Reason  string
}
```

零值是「按原样推送」，所以只在某些情况下干预的 resolver 其余情况返回 `Decision{}` 就行。

## 四个 Action

certpulse 的 `pushJob` 有四个分支，框架需要能表达全部四个——这也是为什么 `Decision` 不只是一个 `[]byte`：

| Action | certpulse 的对应情况 | 行为 |
|---|---|---|
| `ActionPush` | 正常 | 推送（可替换载荷与超时） |
| `ActionFail` | target 行没了 | **终态**失败，不消耗剩余 attempt |
| `ActionRequeue` | target 查询失败 | 退回队列，不消耗 attempt |
| `ActionDrop` | target 被禁用 | 框架不再碰它，应用自己写结果 |

**`ActionDrop` 是这里唯一需要解释的**。一个存在但现在不该被处理的 subject（禁用、超出范围、被规则排除），应用通常会自己写一条 `skipped` 结果。框架这时既不能退回（它会立刻回来），也不能判失败（规则排除不是失败）——它必须什么都不做，把 job 的终态交给应用。

`ActionFail` 用 `JobStore.FailTerminal` 而不是 `Fail`：一个 target 已经不存在的 job 永远不可能成功，花掉剩余的 attempt 去抵达同一个终点只会在历史里留下一串看起来像失败的记录——而对一个被删掉的 target，每次尝试还是一次什么都找不到的派发扫描。

`FailTerminal` 是为此新增的端口方法。

## 未知 Action

resolver 返回不认识的 Action 是 resolver 的 bug。框架选择**退回队列**：job 留在队列里，而不是被丢掉或者带着未解析的载荷被推出去。一个有 bug 的 resolver 不该丢活。

## 验收标准

- [x] 替换载荷在 `DispatchOnce` 和 `DispatchNow` 两条路径上都生效
- [x] 零值 `Decision` 保留入队时的载荷（不是清空）
- [x] `ActionFail` 走 `FailTerminal`，不消耗剩余 attempt
- [x] `ActionRequeue` 不消耗 attempt，仍受 requeue 上限约束
- [x] `ActionDrop` 后框架不再改动 job 状态
- [x] 未知 Action 退回队列，有测试
- [x] `FailTerminal` 进了存储契约套件，两个方言都通过
