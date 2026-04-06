# exec-plan

本目录**只承载阶段性执行计划**，不承载任何系统级 plan、roadmap、设计或长期约束。系统级 roadmap 必须放在 `docs/design/ROADMAP.md`。

每一个开发阶段（一次"从拆分到验收"的完整闭环）对应一个目录，而非单个 .md 文件。

## 目录职责

- `active/`：当前正在执行中的阶段目录。
- `completed/`：已完成并归档的阶段目录。

## 阶段目录三件套

```
{active|completed}/{YYYYMMDDHHmm}-{阶段名}/
├── plan.md          ← 该阶段的详细实施计划（拆分模块/Todo/验收）
├── 开发流程.md       ← 该阶段的执行记录与命令结果摘要
└── 测试错误.md       ← 该阶段的测试失败与修复闭环
```

完成后追加一个**同名 .md 完成摘要**：

```
completed/{YYYYMMDDHHmm}-{阶段名}/
├── plan.md
├── 开发流程.md
├── 测试错误.md
└── {YYYYMMDDHHmm}-{阶段名}.md   ← 完成摘要（验收结果 + 偏离 + 遗留）
```

## 命名规则

- 目录名格式：`YYYYMMDDHHmm-{kebab-case-阶段名}`
- 三件套文件名固定：`plan.md` / `开发流程.md` / `测试错误.md`
- 完成摘要文件名 = 目录名 + `.md`
- 示例：
  - `202604061700-phase0-skeleton/`
  - `202604071000-phase1-proto-and-base-pkgs/`
  - `202604081200-phase2-daemon-mvp/`

## 状态流转

1. 在 `active/` 创建阶段目录与三件套（`plan.md` 必须包含背景、模块拆分、Todo、验收）。
2. 执行过程中持续追加 `开发流程.md` 执行记录，把测试失败登记到 `测试错误.md`。
3. 阶段全部 Todo 完成 + `测试错误.md` 中无 OPEN + 验收通过：
   - 在 `plan.md` 中勾选所有 Todo
   - 把整个目录从 `active/` **移动**到 `completed/`
   - 在新位置内**追加同名 `.md` 完成摘要**

## 禁止事项

- ❌ 在本目录放系统级 roadmap、产品设计、长期约束
- ❌ 把多个 Phase 的内容混进一个目录或同一个 `plan.md`
- ❌ 在项目根目录或 `~/.claude/plans/` 维护 plan 的"主副本"
- ❌ 完成后不迁移、不写同名摘要
- ❌ 在 `completed/` 中继续作为活跃计划修改 `plan.md` / `开发流程.md`
- ❌ 重命名已入库的阶段目录
