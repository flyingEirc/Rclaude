# completed

本目录**只存放已完成阶段的归档目录**。每个目录是从 `active/` 整体迁移过来的，并在内部追加一个**同名 .md 完成摘要**。

## 目录形式

```
completed/{YYYYMMDDHHmm}-{阶段名}/
├── plan.md                              ← 来自 active 的原始 plan，Todo 全部勾选
├── 开发流程.md                           ← 来自 active 的最终执行记录
├── 测试错误.md                           ← 来自 active 的最终错误闭环（无 OPEN）
└── {YYYYMMDDHHmm}-{阶段名}.md            ← 同名完成摘要（在归档时新增）
```

## 同名完成摘要必须包含

1. **完成状态**：done / 部分完成 / 提前终止 / 取消
2. **验收结果**：四件套关键命令的输出摘要（`make fmt` / `make lint` / `make test` / `make build` 或等效命令）
3. **与 plan 的偏离**：如有，列出偏离点 + 原因 + 影响
4. **遗留问题**：移交给后续阶段处理的 TODO，或对 ROADMAP 的反馈
5. **关联 commit**：对应的 git commit hash（若已提交）

## 归档前要求

- 已完成 `plan.md` 列出的全部 Todo 并勾选
- `测试错误.md` 中无 `OPEN` 状态条目
- `make fmt` / `make lint` / `make test` 全绿
- 已写好同名 `.md` 完成摘要

## 归档后约束

- 原则上从 `active/` 整体迁移进入，不直接在本目录新建活跃阶段
- 归档后的 `plan.md` / `开发流程.md` / `测试错误.md` 视为只读历史，仅允许补充勘误
- 完成摘要可在后续被新阶段引用，禁止删除
