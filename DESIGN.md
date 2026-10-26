> 历史维护/阶段记录：下文按当时阶段保存，不代表当前发布状态。当前安装、能力与限制以 [README](README.md)、[统一文档](https://fasttls.github.io/) 和实际发布标签为准。

# ftransport F0–F2 设计决策

日期：2026-10-24。状态：F3 fastls/FFI 本地集成候选已实现；F4 开发验证进行中，尚未发布。

## 已决定的边界

`ftransport` 是客户端传输库。依赖方向保持为 `fastls -> ftransport`，底层不导入 fastls、profile、imitate 或 FFI。公共请求和响应继续使用标准库 `net/http` 类型；浏览器产品目录、TLS 指纹生成、Cookie 策略、重定向、业务重试、内容解码和 SSE 解析不进入本库。

首版只实现 HTTP/1.1 和 TLS ALPN 后的 HTTP/2。HTTP/3、WebSocket 协议、服务端和扩展 CONNECT 不属于首版。

## 工具链决定

模块最低版本定为 Go 1.26.0，候选与本地验证版本固定为 Go 1.26.10，并同时在 Go 1.27.3 上验证。

没有保留原计划中的 Go 1.24 最低版本，原因如下：

- Go 的官方支持策略只维护最近两个主要版本；2026-10-24 的受支持分支是 Go 1.26 和 1.27。
- Go 1.24 的最终版本是 1.24.13，已经结束支持。
- 最后一个可直接依赖且声明兼容 Go 1.24 的 `golang.org/x/net` 是 v0.50.0。官方漏洞库确认其 HTTP/2 实现受 GO-2026-4559 影响，并且早于 v0.60.0 的版本还受 GO-2026-6610、GO-2026-6611 等问题影响。
- 固定 `x/net v0.61.0` 可包含这些修复，但其模块最低版本是 Go 1.26.0。

因此，fastls 集成时需要显式把最低 Go 版本从 1.24 提升到 1.26，而不是让依赖解析隐式完成升级。如消费者必须继续使用 Go 1.24，必须另立受维护的安全回移分支并逐项证明补丁，不能让主线使用已知易受攻击的 HTTP/2 基线。

## 实现来源决定

HTTP/1.1 采用范围窄的客户端编码与连接生命周期实现，以 Go 1.26.10 `net/http` 的语义和测试为基线。不复制服务端、路由或无关帮助代码。任何实际移入的 Go 源码保留原文件版权头，并登记到 `SOURCES.md`。

HTTP/2 的首个实现基线固定为 `golang.org/x/net v0.61.0` 中 Go 1.26 使用的 legacy 客户端实现。只迁入客户端所需文件和依赖，保留 BSD-3-Clause 声明。预期的局部改动集中在：

- 有序且可显式为空的初始 SETTINGS；
- 连接窗口增量和线上/内部额度一致性；
- 初始 PRIORITY 与 HEADERS priority；
- 伪头和普通头的确定顺序及 HPACK 串行化；
- 已完成 TLS 连接的单次所有权交接；
- 有界接收缓冲、取消流的迟到 DATA 计量和慢消费者背压。

Go 1.27 起 HTTP/2 的上游 source of truth 已移到标准库 `net/http/internal/http2`。由于该包不可被外部模块导入，ftransport 不使用 `go:linkname`；维护流程跟踪 Go 1.27 及后续标准库修复，并把适用改动回移到可审计的客户端快照。

## 公共契约

当前可编译契约位于 `config.go`、`request_options.go`、`errors.go` 和 `transport.go`。`Transport` 已实现标准 `http.RoundTripper`、`CloseIdleConnections`、幂等完整 `Close` 与只读 `Stats`。

主要约束：

- `Config` 在构造时验证并深拷贝，此后成为连接池身份的一部分。
- `DialContextFunc` 返回一个且仅一个连接。自定义拨号器负责代理、TLS 握手、证书和主机名验证；成功交接后只有 Transport 拥有连接。
- `DialResult` 用标准 `*tls.ConnectionState` 表达归一化 TLS 状态，不要求连接具体类型是 `*tls.Conn` 或 uTLS 类型。
- `HTTP2Settings.Present=false` 表示未指定；`Present=true` 且空列表表示必须发送空 SETTINGS。`ExplicitHTTP2Settings` 保留这个差别。
- `HTTP2Setting.Value=0` 是显式值，不触发 UA 或浏览器默认配置。
- `ConnectionWindowIncrement` 是在 RFC 初始连接窗口 65535 之外发送的 WINDOW_UPDATE 增量，零表示不增加。
- 公共 priority 权重使用 1..256；编码器负责写为线上 `weight-1`。
- `RequestOptions` 通过克隆后的 Request context 传递，不修改原请求，不使用全局变量，也不改变 Body 流所有权。
- `http.Header` 保留逻辑多值；顺序由单独的名称列表描述，不能从 map 遍历推导。
- 默认拒绝服务器推送配置，直到实现能完整消费 PUSH_PROMISE/CONTINUATION 并保持 HPACK 同步。

中立传输规则：

- `NewTransport(Config{})` 使用标准库 H1/H2 和标准 TLS 验证。
- 不读取 `HTTP_PROXY`、`HTTPS_PROXY` 等环境代理；代理路由继续由 fastls 显式拨号能力负责。
- 禁用标准 Transport 的自动 gzip 添加与解压，避免和 fastls 重复解码。
- 自定义拨号、指纹配置和请求级线上选项要求 `Protocols` 明确且只有 H1 或 H2；这使已握手连接只交接一次，不为探测 ALPN 重拨。
- 自定义拨号器收到 `DialRequest`，必须完成代理、TLS、证书/主机名验证，并返回标准化 `tls.ConnectionState`。H2 必须实际协商 `h2`。
- H1 在标准连接池的应用层连接上只缓冲请求头，按静态或请求级名称列表重排并保留正文增量写入；同一连接复用时每个请求通过 trace 重新绑定自己的顺序。
- H2 使用固定 `x/net v0.61.0` 精简快照；初始 SETTINGS、连接 WINDOW_UPDATE、PRIORITY、HEADERS priority、伪头/普通头顺序和请求级 never-indexed 在 HPACK 编码前确定。
- 定制 H2 的线上接收窗口和内部 `inflow` 使用同一配置。显式 SETTINGS 未给出初始流窗口时按 RFC 默认 65535；连接增量零不发送 WINDOW_UPDATE。
- 定制 H2 的 `MaxConnectionsPerHost` 在快照连接池中实施：达到上限后等待现有连接的流容量，不继续拨号；空闲超时正常生效。独立的最大空闲连接计数仍明确拒绝。未启用指纹且配置标准池计数时继续使用标准库 H2 路径。

## 池和取消模型

连接池键至少包含 origin、协议配置、HTTP 指纹、拨号/代理身份和 TLS 信任身份。认证值、Cookie、请求超时和正文不能成为首次请求固化的池默认值，也不能出现在统计或日志中。

同一 origin 的并发首拨共享受控建连工作，但每个等待者保留自己的 context。单个等待者取消不能取消其他等待者。`CloseIdleConnections` 只关闭真正空闲的连接；完整 `Close` 取消本 Transport 自有的活动流、拨号和等待者，并拒绝新请求。

HTTP/2 单流预算不通过 `net.Conn` 全局 deadline 实现。上传和响应 Body 的 Close 必须能解除库自有阻塞；对调用方任意不可取消 Reader 不承诺强制中断。

## F0 探针结论

回环测试已经证明三个前提：

1. 自定义 RoundTripper 可以由真实 `http.Client` 驱动，并在 TCP peer 上产生逐字节确定的 H1 头顺序。
2. 初始 H2 SETTINGS、WINDOW_UPDATE 和 PRIORITY 可以按给定顺序编码，并与独立静态字节向量逐字节相等；显式初始流窗口零未被默认值替换。
3. uTLS 可以完成证书验证与 ALPN，然后把同一已握手连接交给 H2 client connection；两个请求在同一 TCP 连接上复用。

这些结论只证明接口和接入方向可行，不证明生产 H1/H2、流量控制或连接池已经完成。

## F1 验证结果

F1 测试覆盖标准 H1/H2 协商与复用、真实 `http.Client` trace、自定义标准 TLS H1、uTLS H2 交接、H2 兄弟流取消隔离、全双工双向完成计量、共享首拨等待者独立取消、CloseIdle 对活动 Body 的保护、完整 Close 对拨号/响应/阻塞上传源的取消和关闭、无效拨号结果回收、并发幂等关闭、配置深拷贝和无敏感内容 Stats。

## F2 验证结果

F2 真实 TCP peer 测试逐项解码 H1 字段和 H2 帧/HPACK，覆盖 H1 同连接静态顺序与请求级覆盖、显式空 SETTINGS、显式初始流窗口零、有序 SETTINGS、连接窗口、初始 PRIORITY、HEADERS priority、伪头与普通头顺序及 never-indexed。受控 H2 peer 还验证每 origin 连接上限、流槽释放后的复用和等待者独立取消。标准 TLS H2 覆盖 64 MiB 摘要一致的慢消费者、兄弟流取消隔离、连接复用和全双工；既有关闭、阻塞上传与拨号回收测试继续在新路径运行。

本地 `x/net` 快照逐文件记录上游/本地 SHA-256；根测试会验证本地列，防止未登记修改。Go 1.27 默认包装标准库内部 H2，快照通过构建标签固定可审计 legacy 路径，不使用 `go:linkname`。

## 下一批工作

F4 发布并固定 ftransport 与 fastls 的审阅提交，运行完整原生 FFI 多平台矩阵、race、稳定性和性能基线；本地集成已完成 profile 配置转换、标准 Request/Response/Cookie、代理、timeout/trace 与 FFI 桥接，并保留 WS/H3 能力。

## F4 本地热点优化（2026-10-25）

首批优化保持公开防御性复制契约不变，内部直接读取 `WithRequestOptions` 存储的不可变值，并预计算静态 HTTP/2 伪头顺序。HTTP/1 头重排不再逐行复制和生成小写名称，也不再无条件复制完整头块；新增基准和逐字节测试约束顺序、调用方指定的大小写、重复值、输入不可变性及头正文同批写入。

Windows amd64、Go 1.26.10、Ryzen 9 7900X 的五轮本地结果：`BenchmarkReorderHTTP1HeaderBlock` 从 1.11–1.19 us、1488 B、40 allocs/op 降为 0.49–0.53 us、1152 B、4 allocs/op；复用连接的端到端 H1 请求从 135 降为 105 allocs/op，约 10.45 KiB 降为 9.83 KiB。端到端耗时受本机调度影响，只把稳定的分配和独立热点结果作为本批证据，不据此声明跨平台吞吐提升。

优化版通过根模块十轮回归、关键 H1/取消/选项隔离一百轮回归，并以临时 Go workspace 注入 Fetch、JSON-RPC、gRPC、MITM：三仓各五轮及四进程组合三轮均通过，未修改消费者依赖文件。Windows 本机缺少 Go race 所需的兼容 C 工具链；竞态仍由 Ubuntu Action 门禁执行，不能把普通测试或交叉编译记为 race 已通过。
