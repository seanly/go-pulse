# 0014 类型化载荷 codec

**里程碑** M4 · **依赖** 0002

## 目标

把 §2 用 `bytes payload` 换来的通用性，在 Go 层把类型安全补回来。

## 问题

框架搬运 `[]byte`，但应用不想到处手写编解码：

```go
// 没有 codec 的样子
func (e *myExecutor) Execute(ctx context.Context, job pulse.Job) (pulse.Result, error) {
    var p CertJobPayload
    if err := json.Unmarshal(job.Payload, &p); err != nil {   // 每个 Executor 都要写
        return pulse.Result{}, err
    }
    ...
    raw, err := json.Marshal(resultPayload)                    // 还要写回去
    ...
}
```

## 不用泛型污染核心

一个诱人的做法是让 `Executor[J, R any]` 泛型化。但类型参数会从 Executor 一路传染到 runner、Agent、API、Options——核心包的每个类型都要带参数，而框架内部根本不关心这两个类型。

**决定：核心保持非泛型，泛型只在边缘。** `pulsecodec` 提供一个适配器，把类型化的函数包成 `pulse.Executor`：

```go
// Typed 把一个类型化的执行函数包成 pulse.Executor。解码失败会变成
// status=error、error_code=payload_decode，而不是让 Executor 处理
// 它本来就无能为力的情况。
func Typed[J, R any](c Codec[J, R], fn func(context.Context, pulse.Job, J) (R, error)) pulse.Executor
```

用起来：

```go
exec := pulsecodec.Typed(pulsecodec.JSON[CertJob, CertResult](),
    func(ctx context.Context, job pulse.Job, p CertJob) (CertResult, error) {
        return prober.Probe(ctx, p)        // 纯业务，没有编解码
    })
```

服务端入队侧对称地给 `pulsecodec.Encode`。

## 两个实现

```go
func JSON[J, R any]() Codec[J, R]                              // 无额外依赖
func Proto[J, R proto.Message]() Codec[J, R]                   // 用已有的 protobuf 依赖
```

JSON 适合起步和调试（载荷在日志里可读）；Proto 适合载荷大、QPS 高的场景。certpulse 的 `cert_json` / `tls_json` 本来就是 JSON，所以它用 JSON codec 是自然选择。

## 解码失败的归属

解码失败是**框架的错**（协议不匹配），不是业务错误。所以它在 `Typed` 里被截住并翻成 `error_code=payload_decode`，Executor 根本看不到——它对一个解不开的载荷无能为力。

这与 issue 0005 里 Executor panic 的处理是同一个取向：库的用户不该为框架层面的失败写样板。

## 验收标准

- [x] `Typed` 包出来的 Executor 能正常跑通编解码往返
- [x] 解码失败 → `status=error`、`error_code=payload_decode`，Executor 未被调用，有测试
- [x] JSON 与 Proto 两个 codec 都有往返测试
- [x] 核心包不因这个包变成泛型
