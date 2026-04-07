# 文档治理说明

本目录是项目唯一的正式文档根目录。

## 分层原则

- `docs/design/`：产品核心设计层。沉淀长期有效的架构、边界、数据流、关键约束、**系统级 roadmap（PLAN.md / ROADMAP.md）**。
- `docs/exec-plan/`：阶段性执行层。**只承载阶段目录三件套**（plan / 开发流程 / 测试错误）与完成摘要，不承载任何系统级 plan。
- `docs/superpowers/specs/`：阶段级设计 spec。用于实现前确认方案，作为 `docs/exec-plan/` 的上游输入，不替代系统级 `design/`。
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

## 当前状态

- 已完成 Phase 0 ~ Phase 5，最新归档阶段为 [`docs/exec-plan/completed/202604070914-phase5-integration-test/`](/e:/Rclaude/docs/exec-plan/completed/202604070914-phase5-integration-test/)
- 当前系统已具备：
  - Daemon / Server / FUSE 主链路
  - 服务端文件树与整文件内容缓存
  - 写透、重命名、删除、截断
  - 跨平台 `inmem` 集成测试矩阵
  - Linux 真 FUSE 自动化冒烟与手动脚本入口
- 当前阶段变更说明和验收摘要，应优先查看最新 completed 阶段目录内的同名完成摘要

## 关键约束

- **系统级 plan/roadmap 一律放 `docs/design/`，禁止放进 `docs/exec-plan/`。**
- **阶段性 plan 一律以"目录三件套"形式放进 `docs/exec-plan/{active|completed}/{时间}-{阶段名}/`，禁止以单 .md 文件形式存在。**
- **完成阶段必须迁移到 `completed/` 并追加同名 `.md` 完成摘要**。
