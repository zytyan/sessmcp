# 本次验证

环境：Linux amd64；实际使用 Go **1.22.1**（go.mod 最低 1.22.1），SSH 依赖固定版本，Telnet 为内部实现，无外部 Telnet 依赖。

- `GOTOOLCHAIN=go1.22.1 go test ./...` 通过。
- `GOTOOLCHAIN=go1.22.1 go vet ./...` 通过。
- `GOTOOLCHAIN=go1.22.1 SESSMCP_LONG_TEST=1 go test -race ./... -count=1 -timeout 4m -v` 通过。
- 实际无输出半开 TCP 端点经 JSON-RPC stdio 调用，**110.00350824 秒**返回 endpoint_idle 错误；等待过程中另一个 ping 及时返回。
- stdout 消费者不读取：**3.00 秒**触发独立输出超时，无 stdio 死锁。
- 后续新增阻塞输入/无确认输入反压测试，并再次通过全套短测试与 race。
- 真实 SSH 协议服务验证 exit-status 0/7、双流、增量、后台命令不重复提交、主机密钥校验、交互 shell。
- 新增独立 Telnet 线上字节测试：分包 IAC、字面 IAC、重复协商/拒绝、禁用后重启选项、终端类型与窗口大小、子协商转义/超限/损坏、半截控制序列前的输出、应用写入和协商回复超时。
- SSH / Telnet 交换机模拟器验证模式切换、分页、ANSI、分包提示符、慢速串口式输出。
- `CGO_ENABLED=0` 交叉编译 Linux amd64、Linux arm64、Windows amd64。ARM64 / Windows 产物在本环境仅验证编译与文件格式，未在目标系统运行；CI 模板包含 Windows 短测试。

构建命令与 CI 详见 README.md、scripts/build.sh、docs/ci.yml（模板，未启用 Actions）。日志中的时间是本次实测，CI 重跑允许正常调度偏差，但必须小于 120 秒。

本次现场反馈更新另外验证：SSH 缺失退出状态后保留 transport 并可继续执行；一个 SSH 输入只出现一次线上字节；CR/LF 分别触发 Enter 的模拟终端产生双警告而单 CR 仅一次；延迟输出通过 sess_read 取回；input_id 重试去重、改变输入拒绝、部分发送状态未知时仍不重放；新参数经 MCP JSON 解码与 schema 暴露。仍未连接真实华为设备，具体 diagnose 双警告根因未证实。
