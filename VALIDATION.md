# 本次验证

环境：Linux amd64；实际使用 Go **1.22.12**（go.mod 最低 1.22.5），连接依赖固定版本。

- `GOTOOLCHAIN=go1.22.12 go test ./...` 通过。
- `GOTOOLCHAIN=go1.22.12 go vet ./...` 通过。
- `GOTOOLCHAIN=go1.22.12 SESSMCP_LONG_TEST=1 go test -race ./... -count=1 -timeout 3m -v` 通过。
- 实际无输出半开 TCP 端点经 JSON-RPC stdio 调用，**110.002351866 秒**返回 endpoint_idle 错误；等待过程中另一个 ping 及时返回。
- stdout 消费者不读取：**3.00 秒**触发独立输出超时，无 stdio 死锁。
- 后续新增阻塞输入/无确认输入反压测试，并再次通过全套短测试与 race。
- 真实 SSH 协议服务验证 exit-status 0/7、双流、增量、后台命令不重复提交、主机密钥校验、交互 shell。
- SSH / Telnet 交换机模拟器验证模式切换、分页、ANSI、分包提示符、慢速串口式输出。
- `CGO_ENABLED=0` 交叉编译 Linux amd64、Linux arm64、Windows amd64。ARM64 / Windows 产物在本环境仅验证编译与文件格式，未在目标系统运行；CI 模板包含 Windows 短测试。

构建命令与 CI 详见 README.md、scripts/build.sh、docs/ci.yml（模板，未启用 Actions）。日志中的时间是本次实测，CI 重跑允许正常调度偏差，但必须小于 120 秒。
