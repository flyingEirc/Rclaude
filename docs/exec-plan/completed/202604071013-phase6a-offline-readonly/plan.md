# Phase 6A — 离线只读降级实施计划

## 阶段目标

在 daemon 断线后的短时间窗口内，保留目录树和已缓存文件内容的只读访问能力；未缓存读取与所有写操作立即失败；TTL 到期后该用户完全下线；同一 `user_id` 重连后恢复 live session。

## 范围

- `pkg/config` 增加 `offline_readonly_ttl` 配置与默认值
- `pkg/session` 增加离线只读状态、TTL 过期判断、断线收尾入口
- `pkg/session/service.go` 断线后不再无条件移除 session
- `pkg/fusefs/view.go` 离线缓存读与写拒绝语义
- `internal/inmemtest` 对齐新的断线生命周期
- 单测与 `inmem` 集成测试覆盖离线窗口、TTL 过期与错误语义

## Todo

- [x] 配置层：新增 `OfflineReadOnlyTTL` 字段、默认值和加载测试
- [x] session 层：增加离线只读状态与 TTL 判定
- [x] manager 层：增加 `HandleDisconnect` 和惰性过期清理
- [x] service/harness：统一走断线收尾逻辑
- [x] fusefs：缓存命中读继续可用，未缓存读/写返回 `ErrSessionOffline`
- [x] 测试：补齐 `pkg/session`、`pkg/fusefs/view_test.go`、`pkg/fusefs/inmem_phase5_test.go`
- [x] 验证：执行 `make fmt`、`make lint`、`make test`

## 验收标准

- daemon 断线后，在 TTL 窗口内目录浏览仍可用
- 已缓存文件可继续读，未缓存读立即失败
- 离线窗口内所有写操作立即失败
- TTL 到期后该用户从 manager 中移除
- 重连后新 live session 替换旧离线视图
- `make fmt`、`make lint`、`make test` 全部通过

## 风险与约束

- 不引入后台清理 goroutine，过期回收只做惰性清理
- 不把预取、敏感文件过滤、限流混入本阶段
- `inmem` harness 需同步对齐生产路径的断线状态流转，否则测试结论会失真
