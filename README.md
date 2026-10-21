# sessmcp

面向 AI Agent 的 Go MCP 工具，支持进程内持久 SSH / Telnet 连接、交换机等交互终端、有界等待与增量输出。**Go 1.22.1+，纯 Go，无 CGO。**

```sh
go build -buildvcs=false -o sessmcp ./cmd/sessmcp
./sessmcp help
./sessmcp serve
./sessmcp serve --log ./audit.jsonl
./sessmcp mcp-skill
./sessmcp install-skill --dir ./sessmcp-skill
```

直接运行 `sessmcp` 只显示帮助，不启动服务。`install-skill` 写入 AI 可读的 `SKILL.md`，不会覆盖已有文件。所有连接在服务进程退出后释放。

MCP 客户端配置（stdio，每行一条 JSON-RPC 消息）：

```json
{
  "mcpServers": {
    "sessmcp": { "command": "/absolute/path/sessmcp", "args": ["serve"] }
  }
}
```

## 工具

| 工具 | 用途 |
|---|---|
| `sess_connect` | 创建连接，返回 session；SSH 默认为非交互式 |
| `sess_exec` | 普通 SSH 命令；使用协议退出状态，合并 stdout/stderr |
| `sess_interact` | 向 SSH 前台 shell / Telnet 发送精确输入，换行需自行包含 |
| `sess_read` | 不发送输入，继续等待、读取输出 |
| `sess_list` | 列出连接、运行与断链状态 |
| `sess_close` | 立即关闭并释放连接，不等待该会话的操作锁 |

**普通 SSH 优先使用 `sess_exec`**：一个持久 SSH transport，每个命令新建 exec channel。通过 `Session.Wait()` 获取 SSH exit-status / exit-signal，不使用结束标记或猜测提示符。各次 exec 不保留 cwd / shell 环境；需要状态时使用同一命令或显式交互 shell。stdout/stderr 合并为到达顺序；两个独立 SSH 流之间不承诺远端的严格时间排序。

SSH 连接示例：

```json
{"protocol":"ssh","address":"server.example:22","user":"ops","key_file":"/home/me/.ssh/id_ed25519","known_hosts":"/home/me/.ssh/known_hosts"}
```

也可以提供 `password`、加密私钥的 `key_passphrase`，或用 `host_key:"SHA256:..."` 固定主机密钥。必须选择 known_hosts、host_key 或显式 insecure=true；默认不跳过验证。

随后调用 `sess_exec`：

```json
{"session":"HANDLE","command":"uname -a","wait":"30s"}
```

若提前返回且 `running:true`，使用 `sess_read` 等待原命令；不要重新提交。非零退出码通过 `exit_code` 返回，与传输故障区分。

## 交换机、路由器、Telnet

SSH 连接设置 `interactive:true`；Telnet 连接天然是交互式。Telnet 登录由 Agent 按 Username / Password 提示符分步完成，不自动猜测设备登录流程。

```json
{"protocol":"telnet","address":"192.0.2.10:23"}
```

查看 connect 返回的初始输出，必要时用 sess_read 等待提示符；随后调用 sess_interact：

```json
{"session":"HANDLE","data":"show version\r\n","waitfor":"(?m)switch[#>]\\s*$","timeout":"800ms","wait":"30s"}
```

匹配 `--More--` 后发送一个空格读取下一页。`data` 保留原样，内部 Telnet 编解码负责 IAC 转义；不自动追加回车。

## 等待与超时

需求中的“与”和“任意条件达成返回”存在歧义，本实现按后者处理，即 **OR**：

| 条件 | 含义 |
|---|---|
| `wait` | 从调用开始计算的固定时间，默认 `5s`，即使还设置了 waitfor/timeout |
| `waitfor` | Go RE2 正则匹配合并输出，支持跨网络包 |
| `timeout` | 本次调用开始后，stdout/stderr 连续无刷新的时长；输入和 SSH 保活不重置它 |
| SSH 完成 | 非交互命令退出时立即返回，无须等上述时间 |

时间支持 `100ms`、`30s`、`1min`、`1m30s` 等正值。`waitfor` 匹配失败也有默认 wait 兜底；可以显式增加 wait。发送输入时只匹配本次输入之后的输出；纯读取时也匹配上次增量读取后已经到达的未读输出，避免漏掉调用间隙到达的提示符。

额外边界：

- **端点 stdout/stderr 静默 110s**：交互会话或运行中 exec 断开并错误返回；不受更长 wait 影响。空闲的普通 SSH transport 不因未执行命令而关闭。
- **单个 RPC 上限 115s**：即使端点持续输出，也必须返回。此时远端命令可能仍在运行，继续读取并检查状态。
- 连接、SSH 握手/建通道、输入写入上限 **10s**；输入阶段还受本次 wait 剩余时间约束。若发送阶段超时则关闭连接，明确提示输入可能部分执行，需先检查状态再重试。
- MCP 取消通知仅取消等待；不会自动重放或终止远端命令。可使用 sess_close 释放整个连接。

断开时结果包含 `connected:false`、`error` 和重连提示；也可用 sess_list 检查。不会自动重连与重放具有副作用的命令。已断开的 handle 保留诊断，调用 sess_close 可释放会话配额。

## 增量、历史与反压

默认返回增量输出，同会话使用一个共享游标。`all:true` 返回保留历史，`tail:100` 选择保留历史的最后 100 行；`offset` 是绝对字节偏移，可配合结果的 `end` 分页。这三种显式读取不推进默认游标。历史最多 1 MiB，单次输出最多 64 KiB；`truncated` / `retained_from` 报告已丢弃历史，`more:true` 表示还可以读取更多。all 的含义是全部**仍保留**的历史，超过响应上限需要分页。

输入最大 4096 字节；同会话只允许一个正在等待/发送的操作，竞争立即返回 busy。对于交互终端，上一笔输入没有产生任何输出时拒绝继续发送普通输入，防止慢串口的内核/设备缓冲持续积压；Ctrl-C、Ctrl-D、Escape 单字符仍可发送。观察到新输出后仍应等到提示符再发送下一条命令：TCP 写成功不表示设备已处理。结果的 `backpressure` / `hint` 明确提示限流；输出积压也会提示降低远端详细程度。

最多 32 会话、32 工具 worker、64 个输出帧；单个输入 JSON 帧最多 128 KiB。stdio 请求读取、远端操作与输出写入独立调度。长命令不会阻塞其他连接、ping、取消或关闭；库操作即使未响应 context，独立 RPC 看门狗也会结束响应，其 worker 配额在真正结束前不会释放。

stdio 的单流本身无法越过不读取的消费者传输数据：如果 stdout 写入阻塞 **3s**，服务结束并退出，客户端需重启服务并重连。不会无限阻塞 stdio，也不会无限创建 writer goroutine。

## 日志

`serve --log PATH` 启用 JSONL 输入审计，包含时间、工具名与参数；认证字段 password / key_passphrase 等递归脱敏。POSIX 文件新建权限 0600，已有文件必须是权限私有的普通文件；Windows 使用目录继承的 ACL，需选择私有目录。审计确认写入后才执行输入；队列满、写入失败或 1s 内未完成则拒绝执行此次输入。日志不写 stdout。

命令与交互 data 作为审计对象原文记录，可能含设备密码或业务秘密；审计路径应由用户妥善保管。Telnet 是明文协议。

## 依赖与 Go 1.22

SSH 连接层复用 `golang.org/x/crypto/ssh v0.33.0`。Telnet 使用 `internal/telnet` 内部实现，无外部 Telnet 依赖，固定使用 **Go 1.22.1** 验证。它处理跨包 IAC、字面 IAC 转义、WILL/WONT/DO/DONT 与重复协商抑制，支持 BINARY、ECHO、SGA、终端类型 vt100 和 80×24 窗口尺寸；拒绝不支持的选项。子协商最多 8192 字节，异常转义或超限会报错断开。应用数据与协商回复都受到独立写入超时保护，已解码输出不会被未完整到达的控制序列扣住。go.mod / go.sum 固定兼容版本，不使用要求更高 Go 版本的最新版 SDK；MCP stdio 层实现所需的 JSON-RPC 方法，支持协商 2024-11-05、2025-03-26、2025-06-18、2025-11-25。

## 编译与测试

```sh
# 指定真实 Go 1.22 工具链验证（第一次会下载工具链）
GOTOOLCHAIN=go1.22.1 go test ./...
GOTOOLCHAIN=go1.22.1 go vet ./...
GOTOOLCHAIN=go1.22.1 SESSMCP_LONG_TEST=1 go test -race ./... -count=1 -timeout 4m

# 一次编译全部目标，CGO_ENABLED=0
GOTOOLCHAIN=go1.22.1 sh scripts/build.sh
```

产物与 SHA256SUMS 位于 dist/：

- sessmcp-linux-amd64
- sessmcp-linux-arm64
- sessmcp-windows-amd64.exe

也可直接使用 `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/sessmcp` 等标准交叉编译命令。

测试启动真实本机 TCP/SSH 服务，使用交换机模拟器覆盖配置模式、接口模式、分页、ANSI、分包提示符、串口慢输出和 Telnet IAC。另有 SSH 双流合并/非零退出、历史分页、输入反压、取消与 stdio 队头阻塞测试。长测试建立不发送任何数据、无限保持 TCP 连接的端点，并从 stdio 发出超过两分钟的等待，断言 120s 前得到错误且 ping 不被阻塞。仓库提供 `docs/ci.yml` CI 模板：使用 Go 1.22.1 执行长测试、race 与三目标交叉编译，Windows 单独运行短测试。当前 GitHub 令牌无 workflow 权限，未启用 Actions；有相应权限时将模板复制到 `.github/workflows/ci.yml` 即可。

相关协议/API：[MCP stdio](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)、[MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)、[Go SSH](https://pkg.go.dev/golang.org/x/crypto/ssh)、[Telnet RFC 854](https://www.rfc-editor.org/rfc/rfc854)、[终端类型 RFC 1091](https://www.rfc-editor.org/rfc/rfc1091)、[窗口尺寸 RFC 1073](https://www.rfc-editor.org/rfc/rfc1073)。
