# 0008 凭据签名与 bootstrap token

**里程碑** M2 · **依赖** 0003

## 目标

`pulsereg` 的身份基础：一次性 bootstrap token、长期凭据的签发与校验、吊销。移植 certpulse `internal/agentreg/{bootstrap,credentials}.go`。

## 安全姿态

certpulse 的一段注释值得照抄进框架文档：

> 没有运维专门为某个 agent 签发过 bootstrap token，就拿不到凭据；凭据的寿命不超过它所属的那一行——吊销是 agent 行上的一个版本计数器，每次认证调用都检查。

## 三条必须保留的性质

**1. 签名密钥从 master key 派生**

轮换 master key 会让所有 agent 凭据失效。这与云凭证 keyring 是同一个性质，运维只需要记住一把钥匙。

**2. 吊销靠 `token_version` 比对，不靠黑名单**

签名有效 ≠ 授权有效。运维吊销后，一个签名完好的旧凭据必须和伪造的一样硬地失败。版本计数器让这件事不需要维护撤销列表。

agent 行被删除时，它的凭据也必须立刻失效（查不到行 → `ErrInvalidCredential`）。

**3. 失败原因不外泄**

未知 token、已用过的 token、过期 token 全部返回同一个 `ErrInvalidBootstrap`。攻击者试探 token 时，学不到自己撞上了哪一种。

这条在实现上容易破——比如为了「友好的错误提示」而区分它们。框架里把这个决定写进错误变量的注释。

## token 只存哈希

明文 token 只在创建时向运维展示一次，之后不可恢复。存储里只有 `sha256` 哈希。

## 与 JWT 的关系

certpulse 用 `golang-jwt/jwt/v5`。框架这里要权衡：引入 JWT 依赖，还是自己用 `crypto/hmac` 签一个紧凑格式？

**决定：自己签**。凭据只在两端之间流转，没有第三方要解析它,JWT 的互操作价值用不上。而 §0001 的依赖纪律说核心模块只依赖 grpc/proto/x-time——为一个内部令牌格式拉一个 JWT 库不值得。

格式：`v1.<base64url(payload)>.<base64url(hmac-sha256)>`，payload 是 `{agent_id, token_version, issued_at}` 的 JSON。

**这是与 certpulse 不兼容的改动**：迁移时 agent 需要重新注册一次。issue 0015 要说明这点（有 bootstrap token 的 agent 会自动重新注册，所以实际影响是一次性的）。

## 验收标准

- [x] 签发的凭据能校验通过，取回正确的 agent_id
- [x] 改签名、改 payload、换 master key → 校验失败，有测试
- [x] `token_version` 推进后旧凭据失效，有测试
- [x] agent 行删除后其凭据失效
- [x] 未知/已用/过期 token 返回同一个错误，有测试
- [x] 存储里不出现明文 token（测试断言这点）
