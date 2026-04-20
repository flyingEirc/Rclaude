# Phase 7 — 双机最小部署闭环与冒烟验证实施计划

## 背景

`docs/design/ROADMAP.md` 的 Phase 7 仍未开始，而当前仓库的 `deploy/` 目录为空。虽然系统已经具备 server、daemon、FUSE 挂载、读写透传与集成测试能力，但缺少一组能直接指导双机首次跑通的最小交付物。

本阶段以上游设计 [`docs/superpowers/specs/2026-04-08-phase7-minimal-deploy-smoke-design.md`](/root/Rclaude/docs/superpowers/specs/2026-04-08-phase7-minimal-deploy-smoke-design.md) 为准，优先交付“Linux Server 机器 + 另一台 Daemon 机器”的最小可验证闭环。

## 阶段目标

为仓库补齐一套最小部署模板、启动脚本和冒烟脚本，使使用者可以按最少步骤完成：

- 在 Server 机器启动 `rclaude-server`
- 在 Daemon 机器启动 `rclaude-daemon`
- 在 Server 机器通过 `/workspace/{user_id}` 验证读、写、重命名、删除链路

## 范围

- `deploy/minimal/server.example.yaml`
- `deploy/minimal/daemon.example.yaml`
- `deploy/minimal/start-server.sh`
- `deploy/minimal/start-daemon.sh`
- `deploy/minimal/smoke-remote.sh`
- `deploy/minimal/README.md`
- 阶段内最小验证：脚本语法检查、真实 smoke 回归、`make fmt`、`make lint`、`make test`

## 范围外

- Dockerfile
- systemd 单元
- TLS / 证书
- 安装器或包管理分发
- 多用户批量部署
- 生产级监控、告警与日志轮转

## 模块拆分

### M1 — `deploy/minimal` 配置模板

- 新增 server 与 daemon 双机样例配置
- 样例字段只保留第一次跑通所需的最小集合

### M2 — 启动脚本

- `start-server.sh` 接收单一配置文件路径
- `start-daemon.sh` 接收单一配置文件路径
- 检查二进制与配置文件存在后前台启动

### M3 — 冒烟脚本

- `smoke-remote.sh <user_id> <expected_file>`
- 验证 `ls`、`cat`、写文件、`mv`、`rm`
- 默认对 `/workspace` 工作，也允许通过环境变量覆盖挂载点

### M4 — README

- 明确区分 Server 机器与 Daemon 机器步骤
- 给出构建、配置、启动和冒烟验证的最小命令顺序
- 补充首次排障要点

## Todo

- [x] 新建 Phase 7 执行目录与三件套
- [x] 新增 `deploy/minimal` 下的双机配置模板
- [x] 新增 `start-server.sh` 与 `start-daemon.sh`
- [x] 新增 `smoke-remote.sh`
- [x] 新增 `deploy/minimal/README.md`
- [x] 执行脚本语法检查与真实 smoke 回归
- [x] 执行 `make fmt`
- [ ] 执行 `make lint`
- [ ] 执行 `make test`
- [ ] 阶段完成后迁移到 `docs/exec-plan/completed/`

## 验收标准

- `deploy/minimal/` 具备可直接复制修改的 server/daemon 样例配置
- `start-server.sh` 与 `start-daemon.sh` 可用单一配置路径启动对应进程
- `smoke-remote.sh` 能验证 `/workspace/{user_id}` 的最小读写闭环
- README 明确区分双机顺序和最常见失败点
- `make fmt`、`make lint`、`make test` 全部通过

## 风险与应对

- 脚本跨 shell 兼容性：统一保持 POSIX `sh`，避免依赖 bash 专有特性
- 首次部署最常见失败是 FUSE 与网络连通：README 直接把排查点前置
- 当前环境无完整双机自动化框架：使用真实挂载 smoke 作为最小闭环证据
