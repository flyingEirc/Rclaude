# Docs Table Of Contents

本文件是 `docs/` 目录的入口地图，只回答一件事：代理应当去哪里找什么。

## 适用范围说明

- 下述 `design` / `exec-plan` / `workflow` 约束，默认只适用于**开发、修复、重构、优化**等**涉及代码、程序逻辑、配置行为、接口行为**的工作。
- 如果任务只是纯文档或仓库元信息维护，例如新增或修改 `README.md`、`LICENSE`、注释、说明文字、排版、链接、拼写等，**不视为一个开发阶段**。
- 对这类纯文档/元信息任务，默认**不要求**新建 `docs/exec-plan/active/{时间}-{阶段名}/` 阶段目录，也**不要求**执行 `make fmt`、`make lint`、`make test`。
- 只有当文档任务同时伴随代码或程序行为变更，或者用户明确要求跑这些命令时，才回到完整阶段流程。

## 先看哪里

处理任务时，按以下顺序查阅：

1. 如果是产品结构、系统边界、核心流程问题，先看 `docs/design/`
2. 如果是当前**开发/优化等涉及代码程序操作**的阶段要做什么、做到哪一步、怎么验收，先看 `docs/exec-plan/active/`
3. 如果是开发流程、质量门禁、命令入口，查看 `docs/workflow.md`
4. 如果是第三方库、框架、SDK、API、协议资料，查看 `docs/reference/`

## 去哪找什么

| 需求类型 | 去哪里找 |
|---|---|
| 产品核心设计 | `docs/design/PLAN.md` |
| 系统级 roadmap 与阶段划分 | `docs/design/ROADMAP.md` |
| 当前正在执行的代码开发阶段 | `docs/exec-plan/active/{时间}-{阶段名}/` |
| 已完成阶段归档 | `docs/exec-plan/completed/{时间}-{阶段名}/` |
| 开发流程与质量门禁 | `docs/workflow.md` |
| 第三方参考资料 | `docs/reference/` |

## 当前关键文档

| 路径 | 用途 |
|---|---|
| `docs/design/PLAN.md` | 远程文件 Agent 的核心设计与方案对比 |
| `docs/design/ROADMAP.md` | 系统级 Phase 0~7 阶段划分、风险、验收基线 |
| `docs/ARCHITECTURE.md` | 当前实现架构、技术栈与目录结构约束 |
| `docs/workflow.md` | 格式化、静态检查、测试与 Makefile 工作流 |

## 执行约束入口

以下约束仅适用于**涉及代码/程序逻辑/配置行为变更**的开发阶段；纯文档或仓库元信息任务默认不适用。

- 开始实现前，先确认对应的 design 文档与 ROADMAP 中该 Phase 的范围。
- 然后在 `docs/exec-plan/active/` 中**新建一个阶段目录**（不是单 .md 文件），放入三件套。
- 阶段目录命名必须为 `YYYYMMDDHHmm-阶段名/`（与文件命名同源）。
- 涉及第三方依赖时，先补充或核对 `docs/reference/`。
- 开发完成前，执行 `make fmt`、`make lint`、`make test`。

## 执行计划（exec-plan）组织规范

`docs/exec-plan/` **只承载代码开发阶段的执行记录**，不承载系统级 plan。系统级 roadmap 一律放 `docs/design/ROADMAP.md`。

### 阶段目录三件套

每一个**涉及代码或程序行为变更**的开发阶段（一次"从拆分到验收"的完整闭环）必须对应一个目录：

```
docs/exec-plan/active/{YYYYMMDDHHmm}-{阶段名}/
├── plan.md          ← 该阶段的详细实施计划（拆分模块/Todo/验收）
├── 开发流程.md       ← 该阶段的执行记录、命令结果摘要、偏离说明
└── 测试错误.md       ← 该阶段的测试失败记录与修复闭环
```

约束：
- **目录名 = 时间 + 阶段名**，格式 `YYYYMMDDHHmm-{kebab-case-阶段名}`
- 三个文件名字固定：`plan.md` / `开发流程.md` / `测试错误.md`
- 一个目录 = 一个阶段，禁止把多个阶段塞进同一个目录
- 禁止再把 plan 写到 `~/.claude/plans/` 或项目根目录；那两处不再是源真相

### 状态流转

```
1. 创建 active/{时间}-{阶段名}/ 目录与三件套
2. 按 plan 推进，开发流程/测试错误持续追加
3. 阶段全部 Todo 完成 + 测试错误清零 + 验收通过
4. 在 plan.md 中勾选所有 Todo
5. 把整个目录从 active/ 移动到 completed/
6. 在 completed/{时间}-{阶段名}/ 目录内追加一个同名 .md 完成摘要：
       completed/{时间}-{阶段名}/{时间}-{阶段名}.md
   该摘要文档描述：
       - 完成状态（done / 部分完成 / 提前终止）
       - 验收结果（关键命令输出摘要）
       - 与 plan 的偏离与原因
       - 遗留问题（如有）
```

最终一个完成阶段在仓库中长这样：

```
docs/exec-plan/completed/202604061700-phase0-skeleton/
├── plan.md
├── 开发流程.md
├── 测试错误.md
└── 202604061700-phase0-skeleton.md   ← 同名完成摘要
```

### 禁止事项

- ❌ 在 `docs/exec-plan/` 中放系统级 roadmap、产品设计或长期约束
- ❌ 把多个 Phase 的内容混进一个 `plan.md`
- ❌ 在项目根目录或 `~/.claude/plans/` 维护 plan 的"主副本"
- ❌ 完成后只更新 active/ 而不迁移到 completed/
- ❌ 完成后不写同名 `.md` 摘要
- ❌ 把纯文档/元信息任务（如 README、LICENSE、纯文字修订）强行当作代码开发阶段处理

## 维护规则

- 当文档迁移、重命名或新增时，优先更新本文件。
- 本文件只做导航，不承载详细设计、计划正文或第三方资料正文。
- 阶段目录的命名一旦写入 git，不允许重命名（保证历史可追溯）。
