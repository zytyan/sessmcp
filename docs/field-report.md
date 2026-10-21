# 网络设备现场反馈复盘

反馈包含：exec 输出后断链、增量空输出诱发重试、提示符不匹配、一次 diagnose 调用出现两次进入视图警告。本次没有真实设备访问和原始审计日志，以下区分代码确认与模拟器验证。

| 问题 | 排查结论 | 更新 |
|---|---|---|
| SSH 通道未返回 exit-status 后整条连接被关闭 | 已从代码确认，旧版把 ExitMissingError 当作 transport 故障 | 保留连接与输出，reason=exit_status_missing、warning、exit_code 缺省；不判断命令成功 |
| 输出延迟被理解为未发送 | 增量输出本身符合约定，但缺少直接发送确认，易误用 | 新增 input_state、input_seq、input_bytes、next_action；空输出也提示继续 sess_read |
| 重复调用相同逻辑输入 | 旧版仅在完全无输出时限制新输入，无法识别客户端重试 | 可选 input_id 在最近 128 ID 窗口内强制去重；改变输入复用 ID 报错，未知发送也保留记录 |
| diagnose 双警告 | 未确认真实设备根因；发送路径没有重发循环 | 加 SSH 线上字节测试，加入 CR/LF 分别触发 Enter 且空行回放上一命令的模拟终端；line 默认 SSH CR 避免多余 LF |
| [host] 正则不匹配 [~host] | 模式与实际提示符不同 | skill/README 增加 ~、*、嵌套视图示例，要求匹配观察到的提示符 |
| diagnose-view 不识别 | 无法从这些资料判定所有型号/版本的命令名 | 不自动修正或补执行；建议观察设备帮助 |

测试中一次原始 data="diagnose\r\n" 只在 SSH 通道发出这 10 个字节；上述模拟器仍能产生两次警告，因为 CR 和 LF 各触发一次 Enter。line="diagnose" 默认发送 diagnose + CR 共 9 字节，同一模拟器只产生一次警告。它证明“两个警告”不能单独证明客户端发了两次命令，**没有证明真实华为设备一定如此处理 CRLF**。

现场复测建议：使用单独测试会话和 `serve --log /private/audit.jsonl`；记录工具参数、input_seq/input_state/input_bytes、start/end 与原始输出。先观察当前提示符，再以 line 和唯一 input_id 发出一次用户授权的命令；只用 sess_read 获取延迟输出。如需比较 CRLF，应先确认该操作在测试设备上允许重复，而不是直接对配置命令重复试验。输入审计记录包含原始参数，可能含敏感内容。

input_id 是有界、进程内去重，不能提供跨连接 exactly-once，也不能控制设备对控制字符或空行的解释。没有真实设备和日志时，双警告根因仍需现场核实。
