# Rclaude

`Rclaude` 是一个远程文件访问系统。它让云端执行环境通过普通文件路径访问用户本地工作区，而不需要改造 `cat`、`sed`、`grep`、`ls` 等 shell 工具的调用方式。

## 核心架构

- 本地 `daemon` 扫描并监听工作区，通过 gRPC 双向流连接 `server`
- `server` 通过 FUSE 在 `/workspace/{user_id}/` 暴露虚拟工作区
- 执行环境直接访问挂载路径，文件读写会被路由回对应用户本地机器

## 当前状态

- 已完成协议、Daemon、Server、FUSE、写操作、缓存、离线只读和预取相关阶段
- 已具备跨平台 `inmem` 集成测试
- 已具备 Linux 真 FUSE 自动化冒烟测试与手动脚本入口

## 仓库结构

- `app/client`：本地 `rclaude-daemon` 入口
- `app/server`：服务端 `rclaude-server` 入口
- `api/proto`：gRPC 协议定义与生成代码
- `pkg/syncer`：Daemon 侧同步主逻辑
- `pkg/session`：Server 侧会话与请求路由
- `pkg/fusefs`：FUSE 文件系统视图
- `docs/`：设计、架构、执行计划、工作流与参考资料

## 开发命令

```bash
make tools
make check
go build ./...
```

## 最小启动方式

Server 配置示例：

```yaml
listen: ":9000"
auth:
  tokens:
    "tok-alice": "alice"
fuse:
  mountpoint: "/workspace"
```

Daemon 配置示例：

```yaml
server:
  address: "127.0.0.1:9000"
  token: "tok-alice"
workspace:
  path: "/absolute/path/to/workspace"
```

启动命令：

```bash
go build -o bin/rclaude-server ./app/server
go build -o bin/rclaude-daemon ./app/client

./bin/rclaude-server --config ./server.yaml
./bin/rclaude-daemon --config ./daemon.yaml
```

说明：`server` 运行侧需要 Linux 和 FUSE 可用环境。

## 文档入口

- 设计与方案：`docs/design/PLAN.md`
- 阶段与验收基线：`docs/design/ROADMAP.md`
- 当前实现约束：`docs/ARCHITECTURE.md`
- 开发流程：`docs/workflow.md`
