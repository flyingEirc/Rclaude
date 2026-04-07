# Phase 6B — 敏感文件过滤实施计划

## 阶段目标

在 daemon 侧引入强制生效的敏感路径过滤规则，确保高风险文件和目录不会进入远端工作区视图；读取相关操作对敏感路径表现为不存在；写入、创建、删除、重命名、截断对敏感路径统一拒绝。

## 范围

- `pkg/config` 增加 `workspace.sensitive_patterns` 配置与加载测试
- `pkg/syncer` 新增统一的敏感过滤组件，合并内置规则与追加规则
- `pkg/syncer/scan.go`、`watch.go`、`handle.go`、`handle_write.go` 统一接入敏感过滤
- `pkg/fusefs` / `inmem` 测试覆盖“读隐藏、写拒绝”语义
- 单测与阶段验证覆盖错误语义、目录子树隐藏、自定义模式追加

## Todo

- [x] 配置层：新增 `Workspace.SensitivePatterns` 字段和加载测试
- [x] 过滤器：实现 `pkg/syncer/sensitive_filter.go` 与对应单测
- [x] 扫描/监听：敏感文件不进入初始文件树，敏感目录不递归监听
- [x] 请求处理：读类返回不存在，写类返回 permission denied，`rename` 双边拦截
- [x] 测试：补齐 `pkg/syncer` 单测与 `pkg/fusefs` / `inmem` 集成回归
- [x] 验证：执行 `make fmt`、`make lint`、`make test`、`go build ./...`

## 验收标准

- `.env`、私钥、证书等内置敏感路径不会进入 server 文件树
- 远端 `ls` / `stat` / `cat` 对敏感路径表现为不存在
- 远端 `create/write/delete/rename/truncate` 命中敏感路径返回权限错误
- 自定义 `workspace.sensitive_patterns` 能在内置规则之外追加生效
- `make fmt`、`make lint`、`make test`、`go build ./...` 全部通过

## 风险与约束

- 内置规则只保留高风险且误伤面较小的模式，不在本阶段引入宽泛内容匹配
- 过滤逻辑必须统一复用，不能在 `scan/watch/handle` 各自维护独立规则
- 不在本阶段混入预取、限流或审计能力
