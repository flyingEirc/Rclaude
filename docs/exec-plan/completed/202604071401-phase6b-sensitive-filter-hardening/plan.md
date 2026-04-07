# Phase 6B Hardening — 敏感过滤绕过修复计划

## 背景与触发

- 对应 [`docs/design/ROADMAP.md`](/root/Rclaude/docs/design/ROADMAP.md) Phase 6 “敏感文件过滤”
- 基于 [`docs/superpowers/specs/2026-04-07-phase6b-sensitive-file-filter-design.md`](/root/Rclaude/docs/superpowers/specs/2026-04-07-phase6b-sensitive-file-filter-design.md) 的既有语义做安全加固
- 触发原因：review 发现 3 个 P1 绕过口
  - 通过可见符号链接别名读取隐藏敏感文件
  - 通过可见符号链接别名写入/截断隐藏敏感文件
  - 通过重命名可见父目录搬运隐藏敏感后代

## 目标与范围

目标：

- 让读操作在跟随符号链接后仍无法读取敏感目标
- 让写、截断、重命名在跟随符号链接或移动目录子树时仍无法修改敏感目标
- 保持既有 Phase 6B 语义不变：读类返回不存在，写类返回 `permission denied`

范围内：

- `pkg/syncer/handle.go` 读取侧真实目标敏感判断
- `pkg/syncer/handle_write.go` 写入侧真实目标敏感判断
- `pkg/syncer` 内新增辅助函数，处理符号链接解析与目录子树敏感检测
- `pkg/syncer/*_test.go` 增补针对三条 review 评论的回归测试

范围外：

- 不改 Phase 6B 内置敏感模式集合
- 不改 server/FUSE 协议
- 不引入额外审计或策略配置

## 模块拆分

### M1. 真实目标路径解析

- 对请求相对路径先做工作区内安全拼接
- 读取侧：解析请求路径落到的真实目标；若目标命中敏感规则，则按不存在返回
- 写入侧：对已有路径解析真实目标；若目标命中敏感规则，则按权限拒绝返回
- 对不存在的新建目标，保留按请求路径本身判敏感的现有规则

### M2. Rename 子树敏感后代拦截

- 在 `rename` 执行前检查源路径
- 若源为目录，递归扫描其子树中的真实相对路径
- 只要命中任一敏感路径，拒绝整个 `rename`

### M3. 回归测试

- 读：`visible.txt -> .env`，`read` 必须失败且返回不存在
- 写：`visible.txt -> .env`，`write` / `truncate` 必须失败且 `.env` 内容不变
- rename：`config/.env` 存在时，`rename config -> moved` 必须失败且原目录仍在

## Todo

- [x] 新增路径解析与目录子树敏感检查辅助函数
- [x] 接入 `handleRead`
- [x] 接入 `handleWrite` / `handleTruncate` / `handleRename`
- [x] 补三类绕过回归测试
- [x] 执行 `make fmt`
- [x] 执行 `make lint`
- [x] 执行 `make test`

## 依赖顺序

1. 先补辅助函数，统一真实目标/目录子树判断
2. 再接入读写与 rename 处理器
3. 最后用回归测试锁定 review 中的三个 P1 场景

## 验收标准

- `read visible.txt` 在 `visible.txt -> .env` 时返回 `no such file`
- `write` / `truncate visible.txt` 在 `visible.txt -> .env` 时返回 `permission denied`
- `rename config -> moved` 在 `config` 内含 `.env` 时返回 `permission denied`
- 现有非敏感正常读写回归不受影响
- `make fmt`、`make lint`、`make test` 通过

## 风险与应对

- `EvalSymlinks` 对不存在路径无效：通过“先判请求路径，再在存在时判真实目标”规避新建路径误判
- 目录子树递归可能引入额外开销：仅在 `rename` 且源为目录时执行
- 错误语义偏移风险：统一复用读类 `ENOENT`、写类 `fs.ErrPermission`
