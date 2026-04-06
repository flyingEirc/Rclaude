# 远程文件 Agent 技术调研方案

## 一、项目背景与目标

### 1.1 核心需求

远端运行一个 AI Agent（OpenAI Codex），用户通过对话方式让 Agent 操作**用户本地机器上的文件**。Agent 的执行工具是固定的 bash/shell 命令（cat、sed、grep、ls 等），无法修改其工具调用方式。

### 1.2 关键约束

- Agent 侧：OpenAI Codex，工具调用方式为 bash/powershell，不可更改
- Agent 认为文件在本地文件系统上，直接通过路径执行 shell 命令
- 文件实际存储在每个用户的本地机器上
- 用户规模：1-20 人小团队
- 本地端需支持跨平台（Linux / macOS / Windows）

### 1.3 要达到的效果

```
用户对话 → Codex Agent 决定操作文件
                ↓
    Agent 执行 bash: cat /workspace/user1/main.go
                ↓
    Server 上该路径必须是一个"真实文件"
    但实际内容来自用户本地机器
                ↓
    结果返回给 Agent → Agent 继续处理 → 回复用户
```

---

## 二、整体架构

### 2.1 系统组成

系统由三个部分组成：

1. **本地 Daemon**（Go 二进制，运行在用户本地机器）
2. **Server**（Go 服务，运行在云端 Linux 机器）
3. **Codex Agent**（OpenAI 提供，运行在 Server 同一台或同网络机器）

### 2.2 架构流程

```
┌─────────────────┐       gRPC        ┌────────────────────────────┐
│   用户本地机器    │  ◄──(双向 stream)──►│        Server (Linux)       │
│                 │                    │                            │
│  ┌───────────┐  │                    │  ┌──────────────────────┐  │
│  │ 本地 Daemon│──┼── 上报文件树 ──────►│  │   连接管理 / 路由     │  │
│  │           │◄─┼── 文件操作请求 ◄────┤  │                      │  │
│  │           │──┼── 返回文件内容 ────►│  └──────────┬───────────┘  │
│  └───────────┘  │                    │             │              │
│                 │                    │  ┌──────────▼───────────┐  │
│  ┌───────────┐  │                    │  │    FUSE VFS 层        │  │
│  │ 真实文件   │  │                    │  │ /workspace/{user_id}/ │  │
│  │ 系统      │  │                    │  └──────────┬───────────┘  │
│  └───────────┘  │                    │             │              │
│                 │                    │  ┌──────────▼───────────┐  │
└─────────────────┘                    │  │   Codex Agent         │  │
                                       │  │   bash: cat/sed/ls... │  │
                                       │  └──────────────────────┘  │
                                       └────────────────────────────┘
```

### 2.3 核心流程

1. 用户本地启动 Daemon，指定工作区目录
2. Daemon 扫描工作区，生成文件树（路径、大小、修改时间、类型）
3. Daemon 主动连接 Server，通过 gRPC 双向 stream 上报文件树
4. Server 收到文件树后，通过 FUSE 在 `/workspace/{user_id}/` 下创建对应的虚拟文件/目录
5. Agent 执行 `cat /workspace/user1/main.go` → 内核触发 FUSE Read 回调
6. FUSE handler 通过 gRPC stream 向对应用户的 Daemon 请求文件内容
7. Daemon 读取本地真实文件，回传内容
8. FUSE 返回内容给 cat 进程，Agent 拿到结果

---

## 三、模块设计

### 3.1 本地 Daemon

#### 职责

- 启动时扫描工作区目录，生成文件树
- 主动连接 Server，上报文件树
- 接收 Server 下发的文件操作请求，执行本地文件读写
- 监听文件变更（fsnotify），实时推送增量更新
- 权限控制：仅允许操作指定工作区目录

#### 技术选型

| 项目 | 选择 | 理由 |
|------|------|------|
| 语言 | Go | 单二进制交叉编译，覆盖 Linux/macOS/Windows |
| 文件监听 | fsnotify | Go 生态标准库，跨平台支持 |
| 通信 | gRPC streaming | 双向通信，protobuf 高效序列化 |
| 配置 | YAML/TOML | 指定工作区路径、Server 地址、认证信息 |

#### 配置示例

```yaml
server:
  address: "your-server.com:9000"
  token: "user-auth-token"
workspace:
  path: "/home/john/projects/iot-adapter"
  exclude:
    - ".git"
    - "node_modules"
    - "vendor"
    - "*.exe"
    - "*.bin"
```

#### 安全边界

- 白名单机制：仅允许操作 workspace.path 下的文件
- 路径校验：防止 `../../etc/passwd` 类路径穿越攻击
- 文件大小限制：单文件读取上限（如 10MB），防止 Agent 读取超大文件
- 敏感文件过滤：自动排除 `.env`、`*_secret*`、私钥等文件
- 写操作确认：可配置是否需要用户确认写操作（可选）

---

### 3.2 通信层（gRPC 协议）

#### 为什么选 gRPC 双向 stream

- 本地 Daemon 在 NAT 后面，Server 无法主动连本地
- Daemon 主动连 Server 建立双向 stream，解决连接方向问题
- Server 通过 stream 下发文件操作请求，不需要本地开端口
- protobuf 序列化高效，适合传输文件内容

#### Proto 定义

```protobuf
syntax = "proto3";
package remotefs;

// ============ 数据结构 ============

message FileInfo {
  string path = 1;           // 相对于工作区的路径
  int64 size = 2;
  int64 mod_time = 3;        // Unix timestamp
  bool is_dir = 4;
  uint32 mode = 5;           // 文件权限
}

message FileTree {
  repeated FileInfo files = 1;
}

// ============ 文件变更事件 ============

enum ChangeType {
  CREATE = 0;
  MODIFY = 1;
  DELETE = 2;
  RENAME = 3;
}

message FileChange {
  ChangeType type = 1;
  FileInfo file = 2;
  string old_path = 3;       // RENAME 时的旧路径
}

// ============ 文件操作（Server → Daemon） ============

message FileRequest {
  string request_id = 1;
  oneof operation {
    ReadFileReq read = 2;
    WriteFileReq write = 3;
    StatReq stat = 4;
    ListDirReq list_dir = 5;
    DeleteReq delete = 6;
    MkdirReq mkdir = 7;
    RenameReq rename = 8;
  }
}

message ReadFileReq {
  string path = 1;
  int64 offset = 2;          // 可选，分段读取
  int64 length = 3;          // 0 表示读取全部
}

message WriteFileReq {
  string path = 1;
  bytes content = 2;
  bool append = 3;
  uint32 mode = 4;           // 文件权限，0 表示保持原有
}

message StatReq { string path = 1; }
message ListDirReq { string path = 1; }
message DeleteReq { string path = 1; }
message MkdirReq { string path = 1; bool recursive = 2; }
message RenameReq { string old_path = 1; string new_path = 2; }

message FileResponse {
  string request_id = 1;
  bool success = 2;
  string error = 3;
  oneof result {
    bytes content = 4;        // ReadFile 返回
    FileInfo info = 5;        // Stat 返回
    FileTree entries = 6;     // ListDir 返回
  }
}

// ============ 双向 Stream ============

// Daemon → Server 方向的消息
message DaemonMessage {
  oneof msg {
    FileTree file_tree = 1;       // 初始上报
    FileChange change = 2;        // 增量变更
    FileResponse response = 3;    // 文件操作响应
    Heartbeat heartbeat = 4;
  }
}

// Server → Daemon 方向的消息
message ServerMessage {
  oneof msg {
    FileRequest request = 1;      // 文件操作请求
    Heartbeat heartbeat = 2;
  }
}

message Heartbeat {
  int64 timestamp = 1;
}

// ============ Service ============

service RemoteFS {
  // 建立双向 stream，Daemon 连 Server
  rpc Connect(stream DaemonMessage) returns (stream ServerMessage);
}
```

#### 连接管理

- Daemon 启动后主动连接 Server，带上认证 token
- 连接断开后自动重连，指数退避策略（1s → 2s → 4s → ... → 30s 上限）
- 心跳保活：每 15 秒双向心跳，30 秒无响应判定断线
- Server 为每个连接维护一个 session，映射 user_id → stream 连接

---

### 3.3 Server 端

#### 职责

- 管理多个 Daemon 连接（1-20 个用户）
- 维护每个用户的文件树（内存中）
- 通过 FUSE 挂载虚拟文件系统，供 Agent 的 bash 命令访问
- 路由 FUSE 请求到对应用户的 Daemon 连接
- 文件内容缓存，减少网络往返

#### FUSE 层（核心）

**为什么必须用 FUSE：**
Codex Agent 通过 bash 命令操作文件，它执行 `cat /workspace/user1/main.go` 时，内核需要有一个真实的文件系统响应这个 read 系统调用。FUSE 允许在用户空间实现文件系统，拦截内核的 VFS 调用，转发为网络请求。

**FUSE 库选型：**

| 库 | 优势 | 劣势 |
|------|------|------|
| hanwen/go-fuse | 性能好，底层 API 可控，活跃维护 | API 较底层，学习曲线陡 |
| bazil.org/fuse | API 简洁，文档友好 | 维护不活跃，性能一般 |
| winfsp/cgofuse | 跨平台（含 Windows），基于 CGO | 依赖 C 库，编译复杂 |

**推荐：hanwen/go-fuse**。小团队场景性能足够，API 虽然底层但可控性强。

**需要实现的 FUSE 回调：**

| 回调 | 对应操作 | 触发场景 |
|------|----------|----------|
| Lookup | 查找文件/目录 | ls、cat、任何路径解析 |
| Getattr | 获取文件属性 | stat、ls -l |
| Readdir / OpenDir | 列出目录内容 | ls |
| Open | 打开文件 | cat、vim、任何读写 |
| Read | 读取文件内容 | cat、grep、head |
| Write | 写入文件内容 | echo > file、sed -i |
| Create | 创建新文件 | touch、> newfile |
| Mkdir | 创建目录 | mkdir |
| Unlink | 删除文件 | rm |
| Rmdir | 删除目录 | rmdir |
| Rename | 重命名/移动 | mv |
| Setattr | 修改属性 | chmod、truncate |

**FUSE 挂载目录结构：**
```
/workspace/
├── user_001/          ← 用户1的虚拟工作区
│   ├── main.go
│   ├── go.mod
│   └── internal/
│       └── handler.go
├── user_002/          ← 用户2的虚拟工作区
│   ├── index.ts
│   └── package.json
└── ...
```

#### 缓存策略

纯网络调用延迟太高（每次 read 都走 gRPC 往返），必须做缓存：

| 策略 | 说明 |
|------|------|
| 文件树缓存 | 内存中维护完整文件树，Lookup/Getattr 直接返回，不走网络 |
| 内容缓存 | 首次 Read 后缓存文件内容，后续 Read 直接返回 |
| 缓存失效 | Daemon 通过 fsnotify 推送文件变更事件，Server 收到后清除对应缓存 |
| 缓存上限 | LRU 策略，限制总缓存大小（如 256MB），防止内存爆炸 |
| 写透（Write-through） | Write 操作同步下发到 Daemon，完成后更新缓存 |

#### 预取优化

Agent 通常会连续读取多个文件（比如先 ls 看目录，再 cat 几个文件）。可以做简单的预取：当 Readdir 被调用时，异步预取该目录下所有文件的内容（限小文件，如 < 100KB），这样后续的 Read 大概率命中缓存。

---

## 四、技术风险与应对

### 4.1 延迟问题

**风险：** bash 命令是同步阻塞的，cat 一个文件要等 FUSE → gRPC → 本地读取 → 回传整个链路完成。如果用户在国内，Server 在海外，延迟可能达到 200-500ms 每次文件操作。

**应对：**
- 缓存是最关键的优化，绝大部分读操作应该命中缓存
- 预取机制减少冷读
- 连接选择就近的 Server 节点
- 对于 Codex Agent 来说，它一次任务通常读几个到十几个文件，总延迟在可接受范围

### 4.2 大文件处理

**风险：** 用户工作区可能有大文件（编译产物、数据文件），全量传输不现实。

**应对：**
- Daemon 配置排除规则（exclude），不上报二进制/大文件
- Server 端文件大小限制，超过阈值的文件返回错误或截断
- 支持分段读取（offset + length），Agent 通常不需要读完整大文件

### 4.3 并发写冲突

**风险：** Agent 写文件时，本地用户同时也在编辑。

**应对：**
- 小团队场景，冲突概率很低
- 简单方案：最后写入者胜（last-write-wins）
- 可选方案：Agent 写操作前检查文件修改时间，如果和缓存的不一致，提示冲突

### 4.4 FUSE 平台限制

**风险：** FUSE 是 Server 端（Linux）的能力，本地 Daemon 不涉及 FUSE。但如果 Server 部署环境不支持 FUSE（如某些容器环境），会有问题。

**应对：**
- 确保 Server 部署环境有 FUSE 支持（`modprobe fuse`）
- 容器中使用需要 `--device /dev/fuse --cap-add SYS_ADMIN`
- 备选方案：如果实在不能用 FUSE，可以用真实文件 + 实时同步代替（见方案对比）

### 4.5 连接断开

**风险：** Daemon 网络断开时，Agent 还在执行操作，FUSE 会阻塞。

**应对：**
- FUSE 回调设置超时（如 10 秒），超时返回 EIO 错误
- 缓存的内容在断线期间仍可读取（降级为只读）
- Server 标记用户状态为 offline，Agent 侧可感知

---

## 五、方案对比

### 方案 A：FUSE 虚拟文件系统（推荐）

Agent 的 bash 命令直接操作 FUSE 挂载的路径，完全透明。

| 维度 | 评估 |
|------|------|
| Agent 兼容性 | ★★★★★ 完全透明，bash 命令无需任何适配 |
| 实现复杂度 | ★★★☆☆ FUSE 回调实现有一定门槛 |
| 性能 | ★★★★☆ 加缓存后性能良好 |
| 部署要求 | ★★★☆☆ Server 需要 FUSE 支持 |

### 方案 B：真实文件 + 实时同步

Daemon 启动时把文件内容全量同步到 Server 本地磁盘，后续通过 fsnotify 增量同步。Agent 直接操作 Server 本地的真实文件。Agent 写文件后，Server 把变更同步回 Daemon。

| 维度 | 评估 |
|------|------|
| Agent 兼容性 | ★★★★★ 真实文件，完全透明 |
| 实现复杂度 | ★★☆☆☆ 不需要 FUSE，纯文件同步 |
| 性能 | ★★★★★ 本地磁盘读写，零延迟 |
| 部署要求 | ★★★★★ 无特殊要求 |
| 磁盘占用 | ★★☆☆☆ Server 需存储所有用户的文件副本 |
| 一致性 | ★★★☆☆ 同步延迟可能导致短暂不一致 |
| 首次同步 | ★★☆☆☆ 大项目首次同步耗时长 |

### 方案 C：Hook bash 命令

不用 FUSE，改为拦截 Agent 的 bash 命令，解析出文件路径后走 gRPC 获取内容。

| 维度 | 评估 |
|------|------|
| Agent 兼容性 | ★★☆☆☆ 需要包装 Agent 的执行环境 |
| 实现复杂度 | ★★★★☆ 解析 bash 命令很复杂，边界情况多 |
| 不推荐 | bash 命令组合无穷多，无法完全覆盖 |

### 推荐结论

**小团队（1-20人）优先考虑方案 B（真实文件同步）。**

理由：实现简单，不依赖 FUSE，性能最好（本地磁盘），小团队的文件量不大（通常几百 MB），Server 磁盘完全扛得住。本质上就是做了一个简化版的 rsync/syncthing。

**如果文件量特别大或对磁盘敏感，再考虑方案 A（FUSE）。**

---

## 六、推荐实施路径（方案 B 优先）

### Phase 1：MVP（2-3 周）

**本地 Daemon：**
- 扫描工作区，生成文件树
- gRPC 双向 stream 连接 Server
- 上报文件树 + 全量文件内容
- 接收 Server 下发的写操作，应用到本地
- fsnotify 监听变更，增量推送

**Server：**
- 接收 Daemon 连接，存储文件到 `/workspace/{user_id}/`
- 接收增量变更，更新本地文件
- Agent 写文件后，检测变更同步回 Daemon
- 基本的认证（token）

**此阶段 Agent 就可以正常工作了 —— 它操作的是 Server 上的真实文件。**

### Phase 2：优化（1-2 周）

- 增量同步优化：只传输变更的文件（基于修改时间 + 文件哈希）
- 大文件排除：配置 exclude 规则
- 断线重连：指数退避 + 增量重同步
- 多用户隔离：目录权限隔离

### Phase 3：增强（可选）

- 冲突检测与提示
- Web UI 查看连接状态
- 操作日志 / 审计
- 如果有需要，升级为 FUSE 方案

---

## 七、技术栈总结

| 组件 | 技术 |
|------|------|
| 本地 Daemon | Go, gRPC, fsnotify, YAML 配置 |
| Server | Go, gRPC, 文件系统操作 |
| 通信协议 | gRPC 双向 streaming, Protobuf |
| 认证 | Token-based（JWT 或简单 Bearer Token） |
| 文件同步 | 全量 + 增量（基于 fsnotify 事件） |
| FUSE（可选） | hanwen/go-fuse（如果升级到方案 A） |
| 部署 | Server: Linux VM / Docker; Daemon: 跨平台 Go 二进制 |

---

## 八、开放问题

1. **Codex 的执行环境**：Codex 的 bash 是跑在你控制的 Server 上，还是 OpenAI 托管的沙箱里？如果是后者，你没法在那个环境里挂载 FUSE 或放文件，方案需要大改。

2. **Codex 的工作区路径**：Codex 执行 bash 时，它的工作目录（pwd）在哪里？能否配置？这决定了文件同步到 Server 的哪个路径。

3. **Codex 的 session 模型**：Codex 是每次对话新建一个沙箱，还是有持久化的环境？如果每次新建，文件需要每次重新同步。

4. **写操作频率**：Agent 主要是读文件理解代码，还是会频繁写文件？写操作的频率影响同步策略设计。