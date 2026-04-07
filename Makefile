SHELL := /bin/bash
MODULE  := flyingEirc/Rclaude
GOFILES := $(shell find . -name '*.go' -not -path './vendor/*')

# ─── 平台相关测试参数 ─────────────────────────────────────
# Windows 下 mingw-w64 8.1 的 libwinpthread 与 Go race runtime 不兼容
# (报 STATUS_ENTRYPOINT_NOT_FOUND / 0xc0000139)，所以本机 Windows 跳过 -race。
# Linux / macOS / CI 仍然启用 race detector。
RACEFLAG := -race
ifeq ($(OS),Windows_NT)
    RACEFLAG :=
endif

# ─── 工具版本 ─────────────────────────────────────────────
GOLANGCI_LINT_VERSION ?= v2.6.2
GOFUMPT_VERSION       ?= v0.7.0
GCI_VERSION           ?= v0.13.5

.PHONY: all fmt lint test build clean tools check proto

# ─── 默认目标：格式化 → lint → 测试 ──────────────────────
all: fmt lint test

# ─── 格式化 ───────────────────────────────────────────────
fmt:
	@echo ">>> gofumpt"
	gofumpt -l -w .
	@echo ">>> gci"
	gci write \
		--section standard \
		--section default \
		--section "prefix($(MODULE))" \
		.

# ─── 静态检查 ─────────────────────────────────────────────
lint:
	@echo ">>> golangci-lint"
	golangci-lint run ./...

lint-fix:
	golangci-lint run --fix ./...

# ─── 测试 ─────────────────────────────────────────────────
test:
	@echo ">>> go test"
	go test $(RACEFLAG) -count=1 -timeout 120s ./...

test-cover:
	go test $(RACEFLAG) -count=1 -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo ">>> coverage report: coverage.html"

# ─── 构建 ─────────────────────────────────────────────────
build:
	go build -v ./...

# ─── 全流程（fmt 写回文件后重新 lint）─────────────────────
check: fmt lint test

# ─── proto 代码生成 ───────────────────────────────────────
# 工具版本由 tools/tools.go 锁在 go.mod，不在此处再次硬编码。
# 生成产物入库（见 ARCHITECTURE.md 实现原则），CI 不强依赖 protoc。
PROTO_DIR   := api/proto
PROTO_FILES := $(shell find $(PROTO_DIR) -name '*.proto' 2>/dev/null)

proto:
	@echo ">>> protoc"
	@if [ -z "$(PROTO_FILES)" ]; then \
		echo "no .proto files under $(PROTO_DIR), skip"; \
		exit 0; \
	fi
	protoc -I=$(PROTO_DIR) \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		$(PROTO_FILES)

# ─── 安装工具链 ───────────────────────────────────────────
tools:
	@echo ">>> installing tools"
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	go install github.com/daixiang0/gci@$(GCI_VERSION)
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
		| sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION)
	@echo ">>> installing protoc plugins (versions locked via tools/tools.go in go.mod)"
	go install google.golang.org/protobuf/cmd/protoc-gen-go
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc
	@echo ">>> done"

# ─── 清理 ─────────────────────────────────────────────────
clean:
	rm -f coverage.out coverage.html
	go clean ./...
