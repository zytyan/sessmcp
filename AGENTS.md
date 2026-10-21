# sessmcp 开发约束

- 优先复用成熟连接库：SSH 使用 golang.org/x/crypto/ssh，Telnet 使用 github.com/ziutek/telnet；禁止自写连接协议。
- Go 1.22（最低补丁版本 1.22.5），纯 Go、CGO_ENABLED=0；依赖不得要求高于 Go 1.22。
- 交叉编译 linux/amd64、linux/arm64、windows/amd64。
- 进程内持久 SSH/Telnet 连接；无需重启恢复。标准 SSH 优先 exec，以 SSH exit-status/exit-signal 判断完成，合并 stdout/stderr，禁止结束标记猜测。
- 交互等待支持 wait 固定时间（从调用开始）、waitfor 正则、timeout 输出静默时间；按用户“任意条件达成返回”实现 OR。默认 wait=5s，可显式覆盖；任何调用不得无限等待。
- 独立端点静默保护、连接/握手/写入超时、RPC 上限、stdio 并发调度、有界队列及有界历史，防止半开连接和队头阻塞。两分钟无端点数据必须错误返回，整个调用小于两分钟。
- 默认增量输出；all/tail 可读取保留的历史，并明确报告截断/丢失；缓慢端点必须返回反压信息，禁止无限输入堆积。
- 断开连接要在工具结果中明确提示重连；不自动重放可能产生副作用的命令。
- 无子命令只显示用途；serve 显式启动；help 供人类，mcp-skill 和 install-skill 供 AI。
- 可选 --log JSONL 审计 AI 输入；认证字段脱敏；审计文件权限 0600，stdout 专供 MCP。
- 必须测试：真实 SSH 协议退出与双流、Telnet IAC 分包、交换机模拟器（分页、模式、慢输出）、半开端点、stdio 并发/取消/慢消费者与反压、race。
- 用户已授权创建公开 GitHub 仓库与推送本项目。发布前不得包含凭据、审计日志或本地配置。
