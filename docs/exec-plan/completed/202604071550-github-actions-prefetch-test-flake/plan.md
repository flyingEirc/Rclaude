# GitHub Actions 预取测试抖动修复计划

## 背景与触发

- GitHub Actions 在 `make test` 阶段失败，失败用例为 `pkg/fusefs` 中的 `TestInmem_PrefetchWarmsCacheAndChangeInvalidates`
- 失败现象为读取请求计数偶发比预期多 1：首次读取期望 `1` 实际 `2`，变更后再次读取期望 `2` 实际 `3`
- 根据本地复现，问题只在并发时序更敏感的场景稳定出现，指向测试用例对“预取完成”的判断条件不充分

## 目标与范围

目标：

- 修复 `pkg/fusefs` 预取集成测试的并发抖动
- 保持当前预取实现语义不变，只让断言与真实异步行为对齐
- 重新执行仓库门禁，确认 CI 对应路径恢复稳定

范围内：

- 调整 `pkg/fusefs/inmem_phase6c_test.go` 的等待条件与断言方式
- 补充本阶段执行记录与测试错误闭环
- 运行 `make fmt`、`make lint`、`make test`

范围外：

- 不修改预取策略、缓存策略或会话变更处理逻辑
- 不调整 GitHub Actions 工作流结构
- 不新增与本次抖动无关的重构

## 核心判断

- `startPrefetch` 通过 goroutine 异步拉取内容
- 现有测试只等待 `ReadRequestCount() == 1`，这只能说明“预取请求已发出”，不能保证“缓存已写入”
- 在 `-race` 等更慢调度下，`readChunk` 可能在缓存写入前执行，从而再发起一次读取，导致断言偶发失败
- 因此应等待缓存中出现目标内容，再验证后续读取不增加计数

## Todo

- [x] 创建阶段目录与三件套
- [x] 修正预取测试等待条件，改为等待缓存已热
- [x] 运行定向并发复现验证，确认不再抖动
- [x] 执行 `make fmt`
- [x] 执行 `make lint`
- [x] 执行 `make test`
- [x] 执行 `make build`
- [x] 归档到 `completed/` 并补完成摘要

## 验收标准

- `env GOCACHE=/tmp/go-build go test -race ./pkg/fusefs -run TestInmem_PrefetchWarmsCacheAndChangeInvalidates -count=20` 通过
- `make fmt`、`make lint`、`make test` 通过
- `make build` 通过
- 修复只影响测试稳定性，不改变预取功能行为
