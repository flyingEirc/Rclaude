# 远程 Claudecode 交互方案（Go 实现）

## 1. 核心目标

- 本地启动命令 `***** -claude`
- 远程启动 Claudecode 并分配伪终端 (PTY)
- 双向流转：
  - 本地终端 stdin → 远程 Claudecode stdin
  - 远程 stdout/stderr → 本地终端显示
- 支持终端大小调整、光标控制、ANSI 输出等
- 本地无需安装 Claudecode

---

## 2. 架构设计

```
+-------------------+        TCP / TLS / WS         +------------------------+
|  Local Terminal   | <-------------------------->  |  Remote Server         |
|  ***** -claude    |                               |                        |
|  PTY Input/Output |                               |  Claudecode PTY        |
|  stdin/out        |                               |  stdin/out             |
+-------------------+                               +------------------------+
```

### 模块说明

1. **Local Terminal Client**
   - Go 程序启动本地终端
   - 捕获 stdin/out，建立网络连接到远程 server
   - 发送输入事件、接收输出显示到终端

2. **Remote Server**
   - Go 程序监听网络端口
   - 为每个客户端分配 PTY
   - 启动 Claudecode 进程，挂到 PTY 上
   - 处理数据流（stdin/stdout/stderr）与本地客户端通信

3. **流协议**
   - 简单自定义协议：
     ```text
     [data type: 1 byte][payload length: 4 bytes][payload]
     data type: 1=stdin, 2=stdout, 3=stderr, 4=resize
     ```
   - 保证输入输出完整、支持终端控制

---

## 3. 关键实现模块

### 3.1 远程 PTY 启动

```go
import (
    "os/exec"
    "github.com/creack/pty"
)

func startClaudePTY() (*os.File, *exec.Cmd, error) {
    cmd := exec.Command("claude")   // 启动远程 Claudecode
    ptmx, err := pty.Start(cmd)         // 分配 PTY
    if err != nil { return nil, nil, err }
    return ptmx, cmd, nil
}
```

- PTY 会模拟终端行为，包括行缓冲、光标、ANSI 输出
- stdout/stderr 直接从 PTY 读取即可

### 3.2 本地终端捕获

```go
import "golang.org/x/term"

func setupLocalTerminal() (*term.State, error) {
    oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
    if err != nil { return nil, err }
    return oldState, nil
}
```

- 将本地终端设置为 raw 模式，保证输入实时发送到远程
- 程序退出时恢复原始终端状态

### 3.3 双向流转

```go
func forwardStreams(localIn io.Reader, localOut io.Writer, conn net.Conn) {
    go io.Copy(conn, localIn)   // 本地输入 → 远程 PTY
    go io.Copy(localOut, conn)  // 远程 PTY 输出 → 本地终端
}
```

- `conn` 可以是 TCP/TLS/WS
- 支持多客户端时，每个客户端独立 PTY

### 3.4 终端大小同步

- 当本地终端 resize 时：
```go
type ResizeMsg struct { Width, Height uint16 }
```
- 发送到远程，远程调用：
```go
pty.Setsize(ptmx, &pty.Winsize{Cols: width, Rows: height})
```
- 保证 Claudecode UI 正确渲染

### 3.5 网络协议设计（可选）

- 简单协议示例：
```text
+-----------+-------------------+------------------+
| 1 byte    | 4 bytes           | payload          |
| msg_type  | payload_length    | actual data      |
+-----------+-------------------+------------------+
msg_type: 1=stdin, 2=stdout, 3=stderr, 4=resize
```
- 可以用 Go 的 `encoding/binary` 读写固定长度 header + payload

---

## 4. 本地使用方式

```bash
***** -claude （--server <remote_host>:<port>，这里可进行配置文件写入）
```

- 本地程序启动
- 连接远程 server
- 远程 Claudecode PTY 启动
- 输入输出实时透传
- 整个终端看起来像直接运行 Claudecode

---

## 5. 优化建议

1. **安全**：使用 TLS 或 SSH 通道
2. **多用户**：每个客户端单独 PTY
3. **缓存**：可以针对大屏输出做缓冲优化
4. **自动重连**：网络断开时重连 PTY 保持会话

