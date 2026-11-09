# go-netops 开发说明

本模块是 go-vxlan、go-wireless 共用的运维领域内核，用法见 [README.md](README.md)。

## 边界

- 厂商无关、领域无关：不得导入 go-vxlan、go-wireless 或任何厂商语法；领域模型和规则目录属于消费模块。
- 只有 `store`、`session` 可以依赖 go-net-connector、go-expect 或文件系统；其余包保持纯逻辑。分层由 `scripts/check-architecture.py` 的 `LAYERS` 表约束，新增包须先登记。
- 规则文案、默认严重级别、依赖维度放在 `RuleMeta`，不在规则体内拼接。
- 不引入 `init` 注册或包级可变 registry；`Catalog` 显式构造。

## 不可破坏的语义

- 证据不完整的规则必须被跳过（`insufficient_evidence`），不能产出结论。
- `apply` 状态机：发送前持久化 intent；intent 可能已提交后的失败为 `uncertain`；不重试、不重连、不重放、不回滚、不保存。清理失败使成功无效。
- `FinalizeExecution` 先写独立检查点；最终发布失败或中断时恢复检查点，绝不留下未确认的成功。
- 失败码与诊断不包含设备原文、凭据或错误文本。

## 构建与检查

```sh
export GOWORK=off GOFLAGS=-mod=readonly
gofmt -l . ; go vet ./... ; go test -race -shuffle=on ./...
python3 scripts/check-architecture.py
```

依赖使用正式模块版本（connector v0.6.0、expect v0.5.0），不使用 workspace 或持久化 replace。发布 tag 属于对外操作，需明确授权。
