# PTY 协议速查

本文件只做 `RemotePTY` 协议速查；架构边界、部署路径和执行计划分别放在 `docs/ARCHITECTURE.md`、`deploy/minimal/` 与 `docs/exec-plan/`。

源真相：

- `api/proto/remotefs/v1/pty.proto`

相关官方资料与当前锁定版本：

- gRPC `google.golang.org/grpc v1.80.0`
  - <https://grpc.io/docs/what-is-grpc/core-concepts/>
  - <https://grpc.io/docs/languages/go/basics/>
- Protocol Buffers `google.golang.org/protobuf v1.36.11`
  - <https://protobuf.dev/programming-guides/proto3/>
  - <https://protobuf.dev/reference/protobuf/proto3-spec/>

## Service

```proto
service RemotePTY {
  rpc Attach(stream ClientFrame) returns (stream ServerFrame);
}
```

`Attach` 是单个双向流。`pty.proto` 注释约束了首帧顺序：

- 第一条 client frame 必须是 `attach`
- 第一条 server frame 必须是 `attached` 或 `error`

## ClientFrame

| 字段 | 编号 | 说明 |
|---|---:|---|
| `attach` | 1 | 建立 PTY 会话，仅首帧合法 |
| `stdin` | 2 | 写入远端 PTY 的字节流 |
| `resize` | 3 | 终端大小变化 |
| `detach` | 4 | 主动结束附着 |

### AttachReq

| 字段 | 类型 | 说明 |
|---|---|---|
| `session_id` | `string` | 会话 ID；当前只预留字段 |
| `initial_size` | `Resize` | attach 时的初始终端大小 |
| `term` | `string` | 终端类型 |
| `extra_env` | `repeated string` | 预留的附加环境变量 |

### Resize

| 字段 | 类型 |
|---|---|
| `cols` | `uint32` |
| `rows` | `uint32` |
| `x_pixel` | `uint32` |
| `y_pixel` | `uint32` |

## ServerFrame

| 字段 | 编号 | 说明 |
|---|---:|---|
| `attached` | 1 | attach 成功后的首帧 |
| `stdout` | 2 | 远端 PTY 输出字节 |
| `exited` | 3 | 远端进程退出 |
| `error` | 4 | 拒绝 attach 或运行时错误 |

### Attached

| 字段 | 类型 | 说明 |
|---|---|---|
| `session_id` | `string` | 服务端分配的会话 ID |
| `cwd` | `string` | 服务端实际工作目录 |

### Exited

| 字段 | 类型 | 说明 |
|---|---|---|
| `code` | `int32` | 退出码 |
| `signal` | `uint32` | 信号编号 |

## Error.Kind

| 枚举名 | 编号 | 说明 |
|---|---:|---|
| `KIND_UNSPECIFIED` | 0 | 未分类错误 |
| `KIND_UNAUTHENTICATED` | 1 | 认证失败 |
| `KIND_DAEMON_NOT_CONNECTED` | 2 | 对应 `user_id` 没有在线 daemon |
| `KIND_SESSION_BUSY` | 3 | 同一用户已有活动 PTY |
| `KIND_SPAWN_FAILED` | 4 | 服务端拉起 PTY 失败 |
| `KIND_PROTOCOL` | 5 | 帧顺序或 payload 非法 |
| `KIND_RATE_LIMITED` | 6 | attach / stdin 限流命中 |
| `KIND_INTERNAL` | 99 | 内部错误 |

## 最小握手顺序

1. client 建立 `RemotePTY.Attach`
2. client 首帧发送 `ClientFrame.attach`
3. server 首帧返回 `ServerFrame.attached` 或 `ServerFrame.error`
4. attach 成功后，client 发送 `stdin` / `resize` / `detach`
5. server 回传 `stdout`
6. 会话结束时，server 最终返回 `exited` 或 `error`
