# 开发工作流规范

本文档描述本项目的标准开发流程：**格式化 → 静态检查 → 测试**，所有步骤均通过 `Makefile` 统一入口驱动。

---

## 工具链

| 工具 | 版本 | 用途 |
|------|------|------|
| [gofumpt](https://github.com/mvdan/gofumpt) | v0.7.0 | 代码格式化（gofmt 超集，更严格） |
| [gci](https://github.com/daixiang0/gci) | v0.13.5 | import 分组排序 |
| [golangci-lint](https://golangci-lint.run) | v2.1.6 | 静态检查聚合工具 |

首次克隆仓库后，运行以下命令一键安装所有工具：

```bash
make tools
```

---

## 标准流程

```
写代码 → make fmt → make lint → make test
```

日常开发推荐直接运行：

```bash
make all      # 等价于 fmt + lint + test
```

或在提交前做完整校验：

```bash
make check    # 同 make all，语义更明确
```

---

## 各步骤说明

### 1. 格式化（`make fmt`）

依次执行两个工具：

```bash
make fmt
```

**gofumpt** — 在 `gofmt` 基础上增加了更多强制规则，例如：
- 函数体首尾不允许空行
- `var` 块统一格式
- 多行字符串/调用缩进规范

**gci** — 按三段式强制排序 import：

```go
import (
    // 1. 标准库
    "context"
    "fmt"

    // 2. 第三方依赖
    "google.golang.org/grpc"

    // 3. 本项目内部包
    "flyingEirc/Rclaude/pkg/..."
)
```

> gofumpt 和 gci 均会**直接写回文件**，运行后需重新查看 diff。

---

### 2. 静态检查（`make lint`）

```bash
make lint
```

通过 `golangci-lint` 聚合运行，配置见 `.golangci.yml`。启用的检查项分类如下：

| 分类 | 检查器 | 说明 |
|------|--------|------|
| 错误处理 | `errcheck`, `errorlint` | 禁止忽略 error，正确使用 `errors.Is/As` |
| 代码质量 | `govet`, `staticcheck`, `unused` | vet 扩展检查、SA 系列静态分析、未使用代码 |
| 风格 | `misspell`, `whitespace`, `godot` | 拼写、空白、注释句号 |
| 安全 | `gosec` | 常见安全漏洞（SQL 注入、硬编码密钥等） |
| 性能 | `prealloc` | 切片预分配建议 |
| 复杂度 | `gocognit`（≤15）, `cyclop`（≤10） | 圈复杂度限制 |

如果 lint 报告可自动修复的问题（如 import 顺序），可运行：

```bash
make lint-fix
```

---

### 3. 测试（`make test`）

```bash
make test
```

等价于：

```bash
go test -race -count=1 -timeout 120s ./...
```

- `-race`：开启 race detector，强制检测并发问题
- `-count=1`：禁用测试缓存，每次都真实运行
- 超时 120s，单测不得阻塞

生成覆盖率报告：

```bash
make test-cover    # 输出 coverage.html
```

---

## Makefile 速查

| 命令 | 说明 |
|------|------|
| `make all` | fmt + lint + test（默认目标） |
| `make fmt` | 格式化所有 .go 文件 |
| `make lint` | 静态检查 |
| `make lint-fix` | 静态检查并自动修复 |
| `make test` | 带 race 检测的全量测试 |
| `make test-cover` | 生成 HTML 覆盖率报告 |
| `make build` | 编译检查（不产出二进制） |
| `make tools` | 安装工具链 |
| `make clean` | 清理 coverage 产物 |

---

## CI 集成建议

在 CI pipeline 中执行以下步骤（顺序不可颠倒）：

```yaml
- run: make fmt
- run: git diff --exit-code   # 确认格式化后无残留 diff
- run: make lint
- run: make test
```

`git diff --exit-code` 用于检测代码是否在提交前未经格式化——如果有 diff 说明开发者本地没有运行 `make fmt`，CI 直接失败。

---

## 配置文件

- **`.golangci.yml`** — golangci-lint 规则配置，位于项目根目录
- **`Makefile`** — 所有自动化入口，位于项目根目录

修改 lint 规则时只编辑 `.golangci.yml`，不要在 `Makefile` 里堆 lint 参数。
