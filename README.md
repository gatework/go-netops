# go-netops

面向网络运维工具的领域内核：类型化证据、规则化风险评估、配置偏离检测、受控变更执行。厂商无关，供 go-vxlan、go-wireless 等领域模块复用。

```
采集证据 → 类型化模型 → 规则评估(assess) → 偏离(drift) → 纠正计划(change) → 受控执行(apply) → 持久化(store)
```

| 包 | 职责 |
| --- | --- |
| `evidence` | `Status`、`Observation[T]`、`SourceRef`、`Coverage`、规范 JSON 指纹 |
| `assess` | `Severity`、`Category`、`Subject`、`Finding`、`Rule[M]`、`Catalog[M]`、YAML `Policy`、`Engine` |
| `drift` | `Deviation`；三类期望来源：Baseline（声明意图）、Consensus（同角色多数）、Previous（上次快照） |
| `change` | `Change[M]` 接口、不可变 `Plan[M]`（绑定设备与配置指纹）、`Envelope` 持久化与重渲染校验 |
| `apply` | 单计划执行状态机：intent → send → ack → 回读 → `configuration_verified`；失败分 `failed` / `uncertain` |
| `store` | 私有批次目录（0700/0600、原子 fsync，基于 connector `storage`）、执行日志与检查点恢复 |
| `batch` | 多设备有界并发采集（connector `parallel.Map`）、每设备维度摘要、迟到取消/输出失败重发布，摘要与磁盘一致 |
| `session` | connector / expect 错误到稳定失败码的唯一映射 |
| `inventory` | netdisco / inventory 配置的类型化解码与校验、缓存策略、限额、fleet 规则、Postgres DSN 引号、设备解析 |
| `bootstrap` | 文件描述符预算（RLIMIT_NOFILE）、路径解析 |
| `textscan` | 预编译模式缓存、行扫描与 issue 计数、标签→字段表 |

## 规则示例

```go
var nvePeerDown = assess.NewRule(assess.RuleMeta{
	ID:       "vxlan.nve_peer_down",
	Title:    "NVE peer 未处于 Up",
	Severity: assess.SeverityHigh,
	Category: assess.CategoryRuntime,
	Requires: []evidence.Dimension{"nve_peers"}, // 证据不完整时规则被跳过，不下结论
	Verify:   []evidence.Dimension{"nve_peers"}, // 由注入的 profile 解析为复核命令
}, func(in assess.Input[fabric.Fabric]) []assess.Finding {
	var out []assess.Finding
	for _, p := range in.Model.DownPeers() {
		out = append(out, assess.Finding{Subject: assess.NewSubject("device", p.Host).Child("peer", p.Address), Actual: p.State})
	}
	return out
})

catalog, _ := assess.NewCatalog(nvePeerDown /* … */)
policy, _ := assess.DecodePolicy(policyYAML) // 开关、严重级别覆盖、阈值
engine, _ := assess.NewEngine(catalog, policy, profile)
assessment, err := engine.Run(ctx, model, coverage)
```

`Assessment` 按严重级别排序，`Rules` 记录每条规则 ran / skipped / disabled / failed。没有发现项不代表健康，除非对应规则确实运行。

## 安全语义

- `Complete` 表示证据完整，不代表网络健康；不完整证据不能授权变更。
- `apply.Run` 只执行一次：不重试、不重连、不重放、不自动回滚、不保存配置。intent 提交后的任何失败都记为 `uncertain`。
- `change.DecodePlan` 重新渲染变更，持久化文件中的命令必须与渲染结果完全一致。

## 构建与检查

```sh
make check   # gatework Go 规范的统一门禁（见 go-standards/STANDARD.md MOD-5）
```

包导入边界声明在 `architecture.yaml`，由 [go-standards](https://github.com/gatework/go-standards) 的 `archcheck` 检查。
