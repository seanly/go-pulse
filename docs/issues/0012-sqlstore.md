# 0012 参考存储实现（postgres + sqlite）

**里程碑** M3 · **依赖** 0003

## 目标

`pulsestore/sqlstore`：四个端口的 `database/sql` 实现，postgres 与 sqlite 共一套手写查询。

## 为什么框架要给实现

只给接口，等于把整个框架里最难写对的部分（租约 SQL）推给每个下游重写一遍。见 issue 0011 的说明：单条语句、两种方言、竞态不可见。

## 驱动与迁移都不进依赖

```go
// New 接受一个已经打开的 *sql.DB。驱动由应用注册——框架不替它选
// pgx 还是 lib/pq，也不该让只用 agent 侧的下游编译进一个数据库驱动。
func New(db *sql.DB, d Dialect, opts ...Option) (*Store, error)
```

迁移以 `embed.FS` 暴露，应用用自己的工具执行：

```go
// Migrations 是建表语句，按文件名排序执行。应用可以交给 goose、
// atlas，或者自己 Exec——框架不选迁移工具。
func Migrations(d Dialect) embed.FS
```

certpulse 用 goose，别的项目可能用别的，这不该是框架的决定。

## 方言差异只有三处

照搬 certpulse 的做法：查询用 `?` 占位符写一遍，postgres 下由 `rebind` 改写成 `$1..$N`。schema 保持结构一致。真正不能共享的只有：

1. **租约**：`FOR UPDATE SKIP LOCKED` vs 单写者（issue 0011）
2. **`FailJob` 的 CASE NULL 分支**：postgres 推不出时间戳参数的类型（整个表达式退化成 text 然后拒绝赋值），要显式 `?::timestamptz`；sqlite 直接吃裸值
3. **时间精度**：统一 `Truncate(time.Microsecond)` 后写入，否则 postgres 的微秒精度和 sqlite 的字符串存储在往返测试里对不上

这三处都在调用点注释说明,不藏在抽象后面。

## 表名前缀

默认 `pulse_` 前缀（`pulse_agents`、`pulse_jobs`、`pulse_bootstrap_tokens`、`pulse_agent_configs`），`WithTablePrefix` 可改。

前缀存在是为了让框架的表和应用的表在同一个库里不打架。**但前缀不参与 SQL 拼接的用户输入路径**——它只能来自代码里的 Option，不能来自配置文件，否则就是注入点。构造时校验只含 `[a-z0-9_]`。

## 老项目不迁表

certpulse 已有 `agents` / `probe_jobs` 和一堆外键指向它们。它**不用这个实现**，而是在自己的 store 包里写适配器映射到现有列（issue 0015）。

参考实现是给新项目的。这条路径让「接入框架」不需要数据迁移——这是整个方案里最重要的一个让步。

## 测试策略

- sqlite：`:memory:` 跑全部用例,CI 默认跑
- postgres：靠 `PULSE_TEST_POSTGRES` 环境变量开启，缺省 skip
- **往返一致性测试**：同一组用例对两个方言跑一遍，断言 `time.Time`、NULL、`RETURNING` 行为一致。certpulse 有这个探针测试,照搬

## 验收标准

- [x] 四个端口全部实现
- [x] sqlite 全用例通过
- [x] postgres 用例在 `PULSE_TEST_POSTGRES` 下通过，缺省 skip
- [x] 并发租约测试：两个 goroutine 同时 Lease 不会租到同一个 job
- [x] 往返一致性测试覆盖两个方言
- [x] 表前缀校验拒绝非法字符
- [x] 模块不因这个包引入数据库驱动依赖（`go list -deps` 断言）
