# 文档治理说明

本目录是项目唯一的正式文档根目录。

## 分层原则

- `docs/design/`：产品核心设计层。沉淀长期有效的架构、边界、数据流、关键约束、**系统级 roadmap（PLAN.md / ROADMAP.md）**。
- `docs/exec-plan/`：阶段性执行层。**只承载阶段目录三件套**（plan / 开发流程 / 测试错误）与完成摘要，不承载任何系统级 plan。
- `docs/reference/`：第三方参考层。记录外部工具、框架、SDK、协议和官方资料约束。
- `docs/workflow.md`：开发流程与质量门禁基线。
- `docs/ARCHITECTURE.md`：当前实现架构、技术栈与目录结构约束。

## 使用顺序

开发前按以下顺序查阅文档：

1. `docs/design/PLAN.md`（产品设计与方案对比）
2. `docs/design/ROADMAP.md`（系统级阶段划分与验收基线）
3. `docs/exec-plan/active/{时间}-{阶段名}/plan.md`（当前阶段的具体计划）
4. `docs/workflow.md`（执行流程）
5. `docs/reference/`（第三方资料）

统一入口见仓库根目录 `CLAUDE.md`（→ `AGENTS.md`）。

## 关键约束

- **系统级 plan/roadmap 一律放 `docs/design/`，禁止放进 `docs/exec-plan/`。**
- **阶段性 plan 一律以"目录三件套"形式放进 `docs/exec-plan/{active|completed}/{时间}-{阶段名}/`，禁止以单 .md 文件形式存在。**
- **完成阶段必须迁移到 `completed/` 并追加同名 `.md` 完成摘要**。
