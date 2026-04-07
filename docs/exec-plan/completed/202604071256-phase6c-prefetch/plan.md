# Phase 6C — 小文件预取实施计划

## 阶段目标

在 server 侧为目录读取后的典型热路径引入保守的小文件异步预取能力：`Readdir` 成功后后台拉取当前目录内符合条件的小文件内容，写入现有内容缓存，提高后续 `cat/read` 的缓存命中率，同时不改变现有 FUSE 语义和 daemon 协议。

## 范围

- `pkg/config` 增加 `prefetch` 配置与默认值、加载测试
- `pkg/session/manager.go` 增加预取配置透传
- `pkg/session/session.go` 增加轻量 in-flight 去重
- `pkg/fusefs/view.go` / `mount_linux.go` 接入 `Readdir` 成功后的异步预取
- `pkg/fusefs` 单测与 `inmem` 集成测试覆盖预取命中、跳过、失效和重复抑制

## Todo

- [x] 配置层：新增 `ServerConfig.Prefetch`、默认值和加载测试
- [x] session 层：增加预取 in-flight 去重 helper
- [x] manager 层：透传 `prefetch.enabled`、`prefetch.max_file_bytes`、`prefetch.max_files_per_dir`
- [x] fusefs：实现目录读取后的异步预取，不阻塞 `Readdir`
- [x] 测试：补齐 `pkg/config`、`pkg/fusefs/view_test.go`、`pkg/fusefs/inmem` 回归
- [x] 验证：执行 `make fmt`、`make lint`、`make test`

## 验收标准

- `prefetch` 配置可加载且有默认值
- `Readdir` 成功后会对当前目录的候选小文件发起后台整文件读取
- 后续 `Read` 能命中现有 `contentcache`
- `cache.max_bytes<=0`、`prefetch.enabled=false`、离线只读、超阈值文件等场景会正确跳过预取
- 同一路径连续目录读取不会并发重复预取
- 写入、重命名、删除、daemon 变更后缓存仍能按现有链路失效
- `make fmt`、`make lint`、`make test` 全部通过

## 风险与约束

- 不改 proto，不增加 daemon 侧批量预取
- 不引入独立后台 worker 或复杂调度器
- 预取失败不得影响目录读取结果
- 不在本阶段混入限流或指标埋点
