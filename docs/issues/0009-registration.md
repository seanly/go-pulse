# 0009 注册服务与标签合并

**里程碑** M2 · **依赖** 0008

## 目标

`pulsereg.Service.Register`：bootstrap token 换凭据，agent 行落库。移植 certpulse `agentreg.Register`。

## 同名复用同一行

丢了凭据文件（或者换了台机器）再用新 token 入网的 agent，不能变成第二行——否则它的历史就裂成两半了。

所以 `Register` 按 name 查既有行：有则更新，无则创建。`normaliseAgentName` 限制为可见 ASCII 字母、数字、`-`、`_`、`.`，1..64 个，这样名字在日志和 URL 里都好用。

## 被禁用的 agent 不能靠重新注册解禁

运维禁用了一个 agent，重新入网必须不能悄悄撤销这个决定。`Register` 遇到 `status=disabled` 的既有行直接拒绝，要求先去控制台启用。

## 标签三层合并

certpulse 这块的设计值得完整保留：

| 层 | 含义 | 行为 |
|---|---|---|
| `ReportedLabels` | agent 自己声明的 | **整体替换**，不是合并 |
| token 预设标签 | 签发 token 时代为声明的 | 并入 reported |
| `LabelOverrides` | 服务端（运维）的意见 | 最后叠加，冲突时赢 |

**为什么 reported 是整体替换**：agent 发来的是它当前的完整主张，所以它从配置里删掉的键必须在这里也消失。这能安全地整体覆盖，正因为运维的编辑不在这一列里。

**墓碑**：`LabelOverrides` 的值为 nil 表示「删除这个键」，且跨注册持续有效。这是表达「删掉一个 agent 一直在上报的标签」的唯一方式。

**effective labels 写时计算**：标签匹配在派发热路径上，不能每次读的时候重算。

## 注册响应告诉 agent 真相

`RegisterResponse.effective_labels` 返回服务端实际存下的东西。agent 据此在日志里说出「服务端的标签和我本地的不一样」——包括某个键被运维删掉了。

**本地配置文件故意不被改写**:配置文件属于人，不属于程序。agent 只是把分歧说出来。

## 事务边界

claim token 的 `used_by` 有指向 agents(id) 的外键且逐语句检查,所以 agent 行必须先写。两件事在一个事务里：坏 token 导致 claim 失败时，agent 行的插入一起回滚，什么都不留下。

端口方法是 `TokenStore.Enroll`（见 issue 0003），原子性归实现。

## 验收标准

- [x] 同名重新注册复用同一行，历史不分裂，有测试
- [x] 被禁用的 agent 注册被拒
- [x] reported 标签整体替换；运维 override 存活，有测试
- [x] nil override 作为墓碑跨注册生效，有测试
- [x] 坏 token 不留下半个 agent 行，有测试
- [x] 响应里的 effective labels 反映 override 后的真相
- [x] 标签数量/长度超限被拒
