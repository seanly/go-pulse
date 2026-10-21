# 0010 流注册表、心跳与 gRPC 服务端

**里程碑** M2 · **依赖** 0009

## 目标

长连接的服务端一侧：流注册表、心跳驱动的在线判定、离线清扫、gRPC 门面。移植 certpulse `agentreg` 的 `streamRegistry` 与 `grpc.go`。

## 单 agent 单流，新流顶替旧流

重连的 agent 不能让服务端攒下一堆句柄——泄漏的句柄会让一个死掉的 agent 看起来还能派活。

`attach` 直接替换 map 里的条目，并返回一个**只摘除这一个流**的 detach 闭包：

```go
return func() {
    if r.streams[agentID] == entry {   // 身份比对，不是只看 key
        delete(r.streams, agentID)
    }
}
```

新流 attach 后，旧流自己的 detach 跑起来时发现条目已经不是自己了，于是什么都不做。没有这个身份比对，一次重连就会把新流摘掉。

## 在线状态的两种来源

| 来源 | 存哪 | 用途 |
|---|---|---|
| 心跳时间戳 | 存储（agent 行） | 跨进程可见，离线判定的依据 |
| 流是否挂着 | 内存（注册表） | 能不能立刻推消息 |

两者故意分开。多实例部署时，agent 的流只挂在一个实例上，但所有实例都该知道它是活的。

`AgentRuntime` 只报内存里的东西（在线、已生效配置版本、在跑 job 数），`Online=false` 时其余字段无意义。

## 时序常量

心跳 15s，staleness 45s，reaper 15s。45s 容得下两次丢失的心跳,又能让崩掉的 agent 在一分钟内可见。

这三个常量定义在 `pulse` 包，两端 import 同一处。certpulse 现在在 `runner.go` 和 `agentreg.go` 各写了一份（因为 agent 二进制不链接服务端的包），框架正好消掉这个重复。

## 离线不动它的 job

把 agent 标成离线**故意不碰它的 job**：租约自己有过期时间,五秒的网络抖动不该把一个跑得正常的任务重新入队。

## Connect 的三件事

```
认证 → attach（defer detach）→ 立刻记一次心跳 → 推一次配置 → 收消息循环
```

- **立刻心跳**：抖动后重连的 agent 不该等一整个心跳周期才变在线。
- **推一次配置**：把配置投递绑在 session 开始，彻底消掉「agent 启动后多久才有配置」这个窗口。重连是常见路径，顺便就重推了。
- **推配置失败不杀 session**：那是存储的问题，不是流的问题。agent 继续按本地配置跑，下次连接再试。

## 收消息循环必须快速交接

心跳和 ack 都在这个循环里。慢活必须扔到自己的 goroutine，否则这个 agent 的心跳会堵在后面——然后服务端把它判成离线。这条写进 `Sink` 接口的文档。

## 验收标准

- [x] 重连时新流顶替旧流，旧流的 detach 不误删新流，有测试
- [x] 流断开后 `Runtime` 立刻报离线
- [x] 心跳推进存储里的时间戳；reaper 按 staleness 翻状态，有测试
- [x] 离线清扫不动 job
- [x] Connect 时立刻心跳并推一次配置，有测试
- [x] 推配置失败不终止 session
- [x] 认证失败返回 `Unauthenticated`；禁用的 agent 也是
- [x] 各退出路径都摘除注册表句柄（含 panic 路径）
