# ftransport 实施与交接规划

日期：2026-10-24。状态：规划，尚未实现。本文是后续开发会话的执行依据；目标、接口草案和拟执行测试均不能当作现有能力或已通过结果。

## 1. 项目目标与成功标准

为 fastls 建立来源清楚、便于维护的 HTTP 客户端传输依赖，逐步替换 fhttp。优先保证协议正确性、浏览器 HTTP 特征的可配置性、取消与连接生命周期、有界流式传输，以及与标准 Go HTTP 生态协作。

“更好”的首批可验收含义：

- 对 fastls 的 HTTP/1.1、HTTP/2 配置实现显式、可验证的线上行为，不靠 User-Agent 猜测参数。
- 大上传/下载、慢消费者、全双工、SSE 长连接和取消不会因窗口计数、无界预读或连接池生命周期出错。
- 公共请求/响应接口尽量使用标准库类型，减少重复 Cookie、响应转换和跨库类型适配。
- 每一段引入的实现有固定来源版本、许可及变更记录；最终生产依赖闭包移除 fhttp。
- 能持续同步官方修复；性能以同环境基准证明，不预设“比所有 HTTP 库快”。

首版优先用于 fastls，不追求完全复制 net/http 的服务端、路由、调试服务或所有旧 fhttp 导出接口。

## 2. 当前工作区与已知基线

本机仓库位于 `D:/Projects/fastls/ftransport`，远端为 https://github.com/FastTLS/ftransport 。最初克隆为空仓库，本轮只编写规划。

邻接仓库及本次核对版本：

- `../fastls`：`3c19646b30097cb1d74962ba3df7cd2a99130a1a`，分支 v2。其根模块及 profile 等子模块自有代码已采用 Apache-2.0。
- `../fhttp`：`e027ce786984d3e7e831b0d10c54875dec746b35`，分支 v2。完整授权范围尚有未确认项，见 fastls 的 `THIRD_PARTY_NOTICES.md` 和 `third_party/fhttp-provenance.json`。
- `../fastls-ffi-next-phase`：`8ced37557f45fff039e850f75ab8c036905e562d`，分支 next-phase-distribution。包含 bundled wheel、WS Cookie 集成及并行 CI；不能假设这些改动已进入另一个 `../fastls-ffi` 工作目录的 main。
- 该 FFI 候选固定的 fastls 功能版本为 `7dd6fcd9bdc4f47bae145afab52c70a23f7eb701`；fhttp 为上述 e027ce7；imitate 为 `28b32d452256e3acbdb2e904869e5c4331bf0177`。
- [FFI 候选完整回归](https://github.com/FastTLS/fastls-ffi/actions/runs/40073970900)覆盖六组平台/工具链、源码及 wheel 共 2292 项 SDK 检查，其中 WS 300 项，以及独立 C 调用、竞态和 300 秒稳定性测试。这是旧后端基线，不证明新库通过。
- [旧后端基准短版](https://github.com/FastTLS/fastls-ffi/actions/runs/40073970898)只用于正确性回归，不是正式性能排名。

开始实施时重新记录各仓库 HEAD、分支、dirty 状态和工具链。原工作区中有 Node.js SDK、CI/文档及 fhttp 示例模块的其他未提交改动；不得覆盖、顺手提交或把它们计入本项目交付。可使用独立 worktree 集成。

## 3. 从哪些实现学习

以下为本轮查阅的第一方资料；链接可能随上游更新，实际引入代码前必须固定 tag 和完整提交。没有选定的项目不自动成为依赖。

### 官方 Go：语义、协议状态和维护基线

参考 [net/http](https://pkg.go.dev/net/http)、[x/net/http2](https://pkg.go.dev/golang.org/x/net/http2) 的客户端传输、连接池、报文处理、context、trace、流量控制及测试。优先沿用标准 Request/Response/RoundTripper 语义，复用可独立使用的已授权组件；需要控制线上顺序的地方再做范围明确的官方客户端改造。

官方资料标注这两部分为 BSD-3-Clause。保留每份引入文件的原版权头、LICENSE、必要声明和来源映射；项目自有代码采用 Apache-2.0 不改变它们的授权。

### fhttp：需求和回归行为参考

参考有序头、伪头、SETTINGS/PRIORITY、连接窗口及 fastls 已有调用方式，并把已发现的失败转成独立可观测用例。授权范围未明的 fhttp 实现不复制进新库，也不通过改名、重排或格式化把它当作独立实现。其测试源码也须区分可确认的 FastTLS 自有改动、官方上游内容与未确认来源；优先重新编写黑盒验收。

### fasthttp：分配和对象生命周期

参考 [fasthttp](https://github.com/valyala/fasthttp) 的缓冲复用、分配控制和性能测量方式。保持 io.Reader/io.ReadCloser 的增量消费语义；复用对象须有清楚的所有权和释放时点，不把它的默认整包缓冲模型照搬为本项目默认，也不引用其宣传倍率作为本项目指标。

### req：标准 Transport 接入和可观测性

参考 [imroc/req](https://github.com/imroc/req) 的 Transport 可集成性、协议选择、trace 和调试体验。ftransport 保持传输层职责，业务编解码、自动重试、身份配置生成不随高层客户端功能一起引入。

### Resty：策略分层

参考 [Resty](https://github.com/go-resty/resty) 的请求级配置、扩展点和策略组织，把业务重试、认证、重定向等能力与传输机制区分开。这里借鉴组织方式，不把完整 REST 客户端包装进底层。

参考协议：[RFC 9110](https://www.rfc-editor.org/rfc/rfc9110)、[RFC 9112](https://www.rfc-editor.org/rfc/rfc9112)、[RFC 9113](https://www.rfc-editor.org/rfc/rfc9113)、[HPACK RFC 7541](https://www.rfc-editor.org/rfc/rfc7541)。旧 PRIORITY 指纹须单列兼容行为，不能把旧配置等同于现代 HTTP 优先级语义。

## 4. 与 fastls 的分工及依赖方向

依赖方向保持为 fastls → ftransport；ftransport 不反向导入 fastls、imitate 或 FFI，不包含 `chrome/156/windows` 产品目录。

- **imitate / profile**：产生和描述浏览器配置、UA/Client Hints 及证据。
- **fastls**：解析配置、编译 TLS 配方、uTLS 握手、目标/代理证书信任、代理路由、Session、Cookie 和高层请求生命周期。
- **ftransport**：在给定拨号能力及 HTTP 配置上执行 H1/H2、管理连接、编码报文、处理流量控制、传输请求体和响应体、提供 trace/资源信息。
- **FFI / SDK**：C ABI、句柄、消息就绪通知、Python/Node 等语言生命周期和便捷接口。

ftransport 定义自己最小的有序 HTTP 配置；fastls 将 profile 转换进来，避免底层与产品 schema 互相牵制。确有必要直接依赖独立 `fastls/profile` 时，先写设计决策说明；不得因此引入核心模块循环依赖。

默认使用标准 http.Client 承担 Cookie/重定向等标准高层行为，由 fastls 配置。ftransport 的 RoundTrip 不解析 JSON、multipart 或 SSE 事件，不自行维护第二份 Cookie 罐，不自动启动心跳或业务重试。

## 5. 首版范围

### 必须完成

- 客户端 HTTP/1.1 与 TLS ALPN 协商后的 HTTP/2。
- 可注入的 context 拨号和已完成 TLS 的连接交接，标准 TLS 可作独立使用时的默认路径。
- H1 请求头顺序、H2 伪头/普通头顺序，以及显式 H2 初始配置。
- 并发复用、连接上限、空闲回收、单请求取消隔离和关闭全部自有连接。
- 增量上传/下载、有界背压、H2 全双工、可关闭的流式正文。
- 与 fastls 的阶段预算、代理、Cookie、解码及跨语言回归接通。
- 来源和许可材料、CI、协议/性能证据及维护说明。

### 后续独立阶段

HTTP/3、WebSocket 协议实现、H2/H3 扩展 CONNECT、自动重连、通用服务端、完整服务端推送消费、环境自动代理、跨 origin 连接合并，以及任意非法报文构造。

这不允许迁移时静默破坏 fastls 已有 WS、H3 或旧代理路径。现有 WS 应继续使用其核心拨号路径；现有 H3 如保留，应通过标准类型/独立适配移除 fhttp 引用，能力边界单列。旧接口确需变更时给出迁移记录。

## 6. 公共接口方向：先证明契约，再固定名称

下面是接口设计约束，不是已实现 API，也不是要求逐字照抄的代码模板：

- `Transport` 实现标准 `http.RoundTripper`；输入 `*http.Request`，输出 `*http.Response`，支持 `http.Client{Transport: ...}`。
- 提供构造时深拷贝并校验的固定配置、`CloseIdleConnections()`、幂等的完整关闭能力，以及不泄露凭据的只读统计。
- 固定传输身份包含拨号/TLS/代理身份、协议约束、HTTP 指纹和连接策略；请求级只携带允许变化的线上编码选项与预算。
- 请求级选项可通过克隆后的 Request/context 附加；保留 context 取消、trace、Body、GetBody、ContentLength、Trailer 语义，不以全局变量传递配置。
- `http.Header` 仍表示常规多值头；额外提供有序头描述。原顺序不能从 map 的迭代顺序推导。fhttp 的“特殊头键”在 fastls 迁移适配中转换，不作为真实头发到网络。
- 拨号结果能明确表达 `net.Conn`、已协商协议和标准化的 TLS 状态。底层不要求 uTLS 特定具体类型，也不依赖其 `ConnectionState()` 恰好与标准库签名相同。
- 自定义 TLS hook 明确负责验证证书/主机名和完成握手；标准 TLS 默认验证证书。交接后只能有一个连接所有者，不能为探测 ALPN 重复建连或重复握手。
- 错误可用 `errors.Is/As` 分类，保留原始原因与 context 取消/截止信息；阶段错误由 fastls 对外兼容映射。无效配置尽量在联网前失败。

F0 必须用一个真实 http.Client 集成样例、uTLS 连接交接和 H2 复用用例确认这个方向可行。标准库的 Request.Write/默认 Transport 不保证自定义头顺序；公开 x/net/http2 Transport 的现成配置也不能直接假定能实现所有帧顺序。须抓取实际线上字节，确定需要修改的最小官方代码范围。

## 7. 协议与生命周期的具体要求

### 7.1 HTTP/1.1

- Host/Request.Host、端口、IPv6、URL 转义、查询参数和请求目标形式正确；适用时处理 CONNECT authority-form，不能随意重编码 URL。
- 头名匹配不区分大小写；明确输出大小写和重复值顺序。冲突的大小写重复键、非法名称、CR/LF/NUL、Content-Length/Transfer-Encoding 歧义应明确校验。
- Host、Content-Length、Transfer-Encoding、Connection、Trailer、Expect 等协议管理字段有明确规则；不继承 fhttp 任意覆盖冲突长度的能力。
- 正确处理分块、trailers、HEAD、无正文状态、1xx、早响应、截断、异常关闭与连接复用。消费者提前关闭时只允许有界排空，不能无期限读取以“挽救连接”。
- 未完成响应、上传未终止或 Upgrade 已交接的连接不能放回空闲池。101 交接若支持，保留已经缓冲的升级后字节并明确读写/关闭所有权。

### 7.2 HTTP/2 配置与编码

- SETTINGS 用有序 `(ID, Value)` 列表，区分“未设置”和“明确空/零”；优先拒绝有歧义的重复 ID，已知值校验范围，未知合法 ID 的策略写清楚。
- 显式空配置确实发空 SETTINGS，显式 `INITIAL_WINDOW_SIZE=0` 不能被浏览器默认值覆盖。默认配置从文档化的中立传输配置产生，不读取 User-Agent/Navigator。
- fastls 现有 `ConnectionFlow` 是连接初始窗口 **65535 之外的增量**。新 API 推荐明确命名 `ConnectionWindowIncrement`，零表示不增加窗口，不把它误当绝对窗口值。profile 适配必须验证对应关系。
- 支持指定的初始 PRIORITY 帧和 HEADERS priority，以及合法的伪头顺序；权重 API 与线上的 `weight-1` 转换正确。验证 stream ID、自依赖和请求流号分配，避免优先级配置产生流号冲突。
- H2 头名按协议小写，伪头必须先于普通头；Host/authority 和普通 CONNECT 的伪头集合有专门规则。
- HPACK 在正确顺序上编码，维护连接级状态和并发写序列；提供明确的敏感字段/索引策略。动态表会影响后续压缩字节，不能只比较逻辑头顺序就宣称完整浏览器字节等价。
- 限制帧、头列表、CONTINUATION、缓冲及并发流；处理 PING、SETTINGS ACK、RST_STREAM、GOAWAY、trailers 和协议错误。
- 如配置允许服务器推送，必须正确消费 PUSH_PROMISE/CONTINUATION、同步 HPACK 并采取有界拒绝策略；做不到时明确拒绝该配置。不能忽略头块后继续复用已失步连接。

### 7.3 流量控制：本次替换的重点

- 内部连接/流接收额度必须与实际发出的 SETTINGS、WINDOW_UPDATE 保持一致；处理后续窗口调整、负窗口、上限和溢出。
- 已取消/关闭流收到的延迟 DATA 仍正确消耗/归还连接额度；不能把同一份额度重复退回，也不能漏算 padding 等协议计数。
- 归还窗口必须与受控缓冲/消费策略一致：慢消费者要能形成真实背压，不靠无限接收缓存保持吞吐。
- 不能使用 net.Conn 的全局读写 deadline 实现单个 H2 流的超时，以免关闭同连接上的正常请求。
- 64 MiB 的实际下载、慢消费、随机取消、关闭后迟到帧和并发兄弟流都须通过；不能只检查内部字段或单个小响应。

### 7.4 连接池、取消与重放

- 同 origin 的并发首请求应共享受控的建连过程；等待者有各自 context。一个等待者超时不能取消其它仍在等待的请求，也不能遗留无消费者的建连任务。
- 池键至少包含 origin、协议/HTTP 指纹、拨号/代理/TLS 信任身份。身份由不可变配置/不透明标识区分，不将认证值或 CA 私钥写入日志。请求头、Cookie、超时等请求状态不得固化为首请求的池默认值。
- 默认禁止跨 origin 合并连接；独立 Transport/Session 的隔离规则明确。若共享内部资源，不能共享调用者的凭据或 Cookie 状态。
- `CloseIdleConnections` 不关闭正在下载、上传、双工或等待消费的连接；完整关闭则取消自有活动流、建连和等待者，释放资源且拒绝新请求。
- 文档明确 RoundTrip 失败、响应 EOF、Body.Close、取消及早响应时谁负责关闭上传源和连接。Go 的流式 Body 必须能被并发 Close 解除阻塞；不能承诺中断任意不可取消 Reader，更不能为每次阻塞读取遗留 goroutine。
- 默认不做隐藏业务重试。若保留协议层安全重试，必须有明确证据证明请求未被处理、重放条件满足且次数有界；一次性 Body/GetBody=nil 的上传不因 GOAWAY、REFUSED_STREAM 或断连被盲目重放。

### 7.5 传输预算、流式和可观测性

- 总预算由请求 context 贯穿拨号、代理、TLS、等待连接、上传、响应读取及业务暂停。
- 与 fastls 对齐：Connect/Headers/Read/Write 各自有定义；普通请求的 headers 等待在上传完成后计时，双工需独立定义。Read 的网络等待预算不计业务暂停；Write 的进度按真实传输消费，不按生产者入队重置。
- 标准 `httptrace` 至少覆盖现有 fastls 使用的 GetConn、GotConn、WroteRequest 等事件，补充 DNS/TLS/首字节等事件的支持矩阵；事件不能与实际连接所有权矛盾。
- 不自动添加浏览器 UA、认证或业务重试；透明压缩策略显式化。避免底层和 fastls 各解码一次，保留原始多值头、压缩前后长度和 trailer 语义。
- SSE 只作为长时间增量响应传输；H2 全双工要能在上传未完成时返回响应头、继续同时读写并实现半关闭。
- Stats/trace 默认不包含认证、Cookie、正文或完整敏感 URL；统计字段标明作用域和采样语义。避免为观察性能反过来引入全局热锁。

## 8. fastls / FFI 的实际迁移清单

迁移前写出逐项映射，不能仅搜索替换 import：

- `fastls/client.go`、`index.go`、`session.go`：目前使用 fhttp.Client/Request/Response；`Options.CookieJar`、`Options.HTTP2Settings`、`Response.Client` 还公开暴露了旧类型。换成标准类型/新配置会影响 Go 调用方，须提供迁移文档及独立消费者编译验收，不能声称完全源码兼容。
- `fastls/profile_execution.go`：把 `profile.HTTP2Config` 转为新的有序配置，保留显式零/空、优先级及窗口增量语义。
- `fastls/h2settings.go`：保留必要的旧字符串解析入口，但输出新配置；不要把旧 parser 的默认补全混入新的显式 API。
- `fastls/roundtripper.go`：替换协议探测、缓存连接和 H1/H2 transport 路径，保留 ALPN、证书校验、连接所有权、池隔离和 CloseIdle 行为；移除对旧 fhttp 类型的来回转换。
- `fastls/timeouts.go`：改用标准 trace 与新 transport 的真实进度事件，避免和底层预算重复计时或互相关闭共享连接。
- `fastls/cookie_store.go`：将旧 fhttp.Cookie/CookieJar/cookiejar 迁移到标准类型，保留域名、路径、Secure、过期、公共后缀、显式 Cookie 覆盖及会话隔离。
- `fastls/connect.go`：旧代理路径直接使用 `http2.ClientConn`、NewClientConn 和 H2 CONNECT。先识别仍受支持的公开路径；可评估官方 x/net/http2 的标准类型适配，不要求新库为了旧调用复制全部低层 API。任何保留/调整都必须有代理靶场和兼容说明。
- `fastls/proxy_transport.go`：保留已验证的 HTTP/HTTPS CONNECT、SOCKS5 本地/代理 DNS、代理与目标独立 CA、认证隔离及 context 传播。主机环境代理不应隐式改变行为。
- `fastls/http3_transport.go`：当前也通过 fhttp 类型适配。即使 H3 不在新库首版，也须移除这条旧依赖或明确隔离到不进入最终构建的可选模块；不能只替换 H1/H2 后就宣称依赖已移除。
- `fastls/websocket_context.go`：现有 WS 使用独立的标准 HTTP/Gorilla/uTLS 握手路径；保持 HTTP/1.1 ALPN、证书校验、取消与现有限制，避免迁移 HTTP 时意外影响它。
- FFI 候选的 `internal/bridge/cookies.go`、`internal/bridge/websocket.go` 仍直接导入 fhttp，要改为标准 Cookie/Request/Response 处理。C ABI、JSON 协议、Python/Node 公共行为和就绪通知保持兼容；确需增量变化须单列版本与能力标记。
- 更新各消费者 go.mod/go.sum、开发 workspace、`ci/dependencies.json`、CI 的依赖解析和实现门禁，新增固定 ftransport SHA，移除旧 fhttp 门禁。不得让脚本因为找不到旧 markers 就跳过关键测试。

最终检查 fastls、FFI 实际入口及独立示例的 `go list -deps`、`go list -m all`，不能只检查 go.mod 中有没有 fhttp 字符串；同时检查源树中是否仍复制了不明来源实现。旧后端仅可在独立比较环境中保留，不进入候选产物的编译闭包。

## 9. 分阶段执行与交付

### F0：来源、接口和可行性收口

交付：`DESIGN.md`、上游来源清单、许可材料、API 草案、fastls 导出类型迁移清单、工具链决策，以及三个最小协议探针：有序 H1、定制 H2 初始帧、uTLS ALPN 后单连接交接。

先创建模块与最小测试环境，固定官方来源。公共仓库资料是调研入口，不是可以追踪 latest 的源码供应方式。引入源码记录原模块、完整 commit/tag、原路径、哈希、许可、修改文件及原因；源文件保留原声明，补丁尽量集中在可重放的小范围中。

fastls 当前声明 Go 1.24，已有 CI 还验证 Go 1.24.11/1.26.10。根据 [Go 官方支持政策](https://go.dev/doc/devel/release)，旧版本能构建不等于仍在安全维护期。F0 要核实当时受支持的 Go 分支及相关 HTTP 修复：优先保留现有最低构建兼容性，但不能因此采用已停止维护的协议快照又不评估修复；较新官方源码如无法在旧 Go 上编译，记录选择性回移修复、保持独立快照或协调提高最低版本的成本与决定，不能静默抬高 fastls 的 Go 要求。

退出条件：三个线上探针有独立 peer/字节证据；类型与许可方向明确；实现者知道选择哪份官方客户端代码、需要改哪些内部依赖。不得直接引用 Go 标准库不可导入的 internal 包、依靠未记录的 go:linkname，或把不编译的接口样例当作完成。

### F1：公共契约、配置和测试基础

交付：标准 RoundTripper 骨架、不可变配置校验、request 选项、TLS 拨号接口、trace/错误约定、独立 loopback peer、限额/取消计量和 CI 入口。

先编写配置有效/无效、拷贝与并发、显式空/零、上下文取消、资源所有权及公共消费者编译测试。此时还不能把未实现的协议列为支持。

### F2：H1 与 H2 实现可并行

H1 工作包负责报文、顺序、流式正文和连接复用；H2 工作包负责初始配置、HPACK、流量控制、多路复用和双工。共享的拨号/池接口先固定，避免同时改同一关键文件。平台 CI/靶场可同步推进。

退出条件：各协议的功能、错误路径和回收检查完成；H2 通过真实 64 MiB 下载、慢消费者、取消和兄弟流隔离。仅“能请求成功”不能进入默认迁移。

### F3：fastls 集成与依赖替换

在独立集成分支切换核心与 Cookie/trace/代理适配，随后接通 FFI。保持对外 SDK 调用方式，例如 `chrome/156/windows`，不把浏览器目录搬进底层库。

交付：逐项迁移记录、公开 Go API 的变化说明、固定源码与产物哈希、完整跨仓结果和不含 fhttp 的构建闭包证据。现有 H3/WS/旧代理路径逐项说明保留、修复或不适用；不能静默退化。

### F4：性能、维护和候选发布准备

先比较正确性，再做分配/吞吐优化；用相同条件比较官方传输、旧后端和新后端。记录回归和优势，允许新库在部分场景较慢。

交付：机器可读逐轮结果、可读报告、上游更新流程、变更补丁清单、许可/依赖材料及已验证的平台范围。默认只生成候选 artifact，不自行创建正式 Release、Go 版本标签、PyPI 或 npm 发布。

## 10. 测试与验收要求

### 测试来源与协议证据

- 配置单元测试之外，使用独立的标准库 H1/H2 服务端、受控 TCP/TLS peer 和静态协议向量；错误帧/半关闭等可由独立 wire fixture 注入。
- 有序头和帧顺序核对原始线上字节；H2 HPACK 同时核对独立解码与静态向量。不要只用新库自己的编码器/解析器互证。
- 测试证据包含实际 ALPN、协议、连接 ID、正文长度与摘要、状态/错误、超时阶段和回收采样。公网指纹站点可作补充，不做 CI 唯一依据。
- fhttp 参考用例的行为要覆盖，但源码使用先核对许可。面向服务器的协议检查工具不能直接被称为客户端完整合规证明。

### 必须覆盖的负载与故障

- H1/H2 空响应、HEAD、重复头、trailers、压缩透传、重定向在上层的正确行为、非法头和截断正文。
- 64 MiB 上传/下载完整摘要、并发小请求、慢生产者、慢消费者、提前关闭、上传早响应及 H2 全双工半关闭。
- 连接窗口/流窗口的 0、1024、65535、6291456 等边界；窗口刷新、关闭流延迟 DATA、padding、超额 DATA 和算术边界。
- 单流 RST、GOAWAY、拒绝流、连接丢失、空闲超时、池容量、并发首拨、等待者独立取消、会话关闭和反复重连。
- 目标/HTTPS 代理证书失败、主机名不符、代理认证/拒绝/停滞、SOCKS DNS 模式和凭据不泄露。
- 非法/巨量帧、头列表限制、CONTINUATION 顺序、HPACK 失步和被拒绝推送；自定义缓冲上限及资源耗尽后的恢复。
- 取消时上传源 Read 可被 Close 解阻，原生 worker/goroutine、连接、FD/句柄及缓冲回到约定范围。RSS 采样允许运行时余量，不以一次 GC 后数值证明不存在泄漏。

已知行为参考：

- fhttp：`TestExplicitZeroDoesNotSelectNavigatorDefaults`、`TestExplicitEmptySettings`。
- fhttp：`TestReceiveWindowMatchesWire`、`TestReceiveWindowRefreshSmallAndLarge`、`TestReceiveWindowClosedStreamDataRefund`、`TestReceiveWindowClosedStreamDataOverrun`。
- fhttp：`TestPooledDialCancellationIsolation`、`TestStreamingUploadBlockedProducerStops`。
- fastls：`connection_lifecycle_test.go`、`context_test.go`、`request_lifecycle_test.go`、`stream_upload_test.go`、`duplex_test.go`、`decode_timeouts_test.go`、`cookie_store_test.go`、`proxy_transport_test.go`、`websocket_context_test.go`。
- FFI 候选：`tests/test_stability.py`、`test_network.py`、`test_pooling.py`、`test_cookies.py`、`test_decode_timeouts.py`、`test_upload.py`、`test_duplex.py`、`test_sse.py`、`test_notifications.py`、`test_proxy.py`、`test_request_options.py`、`test_websocket.py`，以及独立 C 程序。

### CI 与反馈速度

每次代码 push/PR 执行固定依赖检查、静态检查、配置/协议测试及相关集成回归；完整候选还需六组原生验收、Linux race/fuzz 冒烟与至少 300 秒稳定性检查。工具链最低版本在 F0 决定后固定到具体补丁版本，初始平台沿用 Windows amd64、Linux amd64/arm64、macOS amd64/arm64及最低版本组合，不把交叉编译成功等同实际运行。

构建一次原生输入，独立 job 运行协议/消费者、源码/wheel、竞态和长测；重负载资源/性能检查不用同一 runner 并发争抢。逐项实时输出日志和保存退出码、耗时、来源及产物哈希；缺失、失败、取消或意外跳过不算通过。

快速检查用于尽早反馈，不能取代最终门禁。文档专用变更可只运行格式/链接/计划一致性检查，分类不明按代码变更处理。缓存用于可验证的依赖/编译产物，不能用上一提交的绿色结论代替新候选的验证。

每个实现批次争取在约 30–60 分钟内给出可检查产物或最小失败用例；超时就拆分任务和记录阻塞原因。这是反馈节奏，不是承诺完成时间，也不是降低验收要求。等待跨平台或长测时推进独立的文档、来源记录或下一工作包。

### 性能比较

参考标准 net/http/x/net/http2、旧 fhttp 与 ftransport，通用传输和浏览器配置分组。保持相同服务器、协议、CA 校验、压缩、正文、并发、连接复用、超时和消费节奏；不能把普通 Go TLS 与浏览器 TLS 的差异当成 HTTP 后端提升。

至少覆盖小请求、复用/新建连接、64 MiB 流式上传/下载、慢消费者、空闲长连接和混合取消。独立进程、多轮轮换顺序，报告成功数、错误率、RPS、MiB/s、首字节/完整响应延迟、CPU、分配、RSS、goroutine/FD/句柄及关闭后状态。先建立基线再设回归阈值；共享 runner 的波动不作为单轮速度淘汰依据。

保留既有正式基准报告，新结果另存并标明来源。首版不预设倍率，也不以减少证书校验、背压或错误统计换取速度。

## 11. 建议的仓库组织与维护材料

在 F0 后按真实需要创建，下面不是当前已存在的目录：

- 根包：Transport、配置、错误与公共请求选项；使用标准 HTTP 类型。
- `internal/h1`、`internal/h2`：协议实现；官方来源与本地扩展边界可追踪。
- `internal/pool`、`internal/testpeer`：确有复用价值的连接管理与独立靶场；不要为少量代码过度拆包。
- `integration/`：标准 http.Client、fastls、uTLS、代理及跨语言消费者验收，不在根模块强制依赖 fastls。
- `benchmarks/`：独立基准驱动、固定配置和原始记录。
- `LICENSE`、`NOTICE`、`third_party/`：自有许可、上游原文、源文件映射、哈希和必要声明。
- `DESIGN.md`、`MIGRATION.md`、`UPSTREAM.md`、`CI.md`：最终设计决策、类型迁移、上游固定版本/补丁及更新流程、复现与验收说明。

更新官方基线时逐个重放必要扩展，审核涉及 parser、窗口、HPACK、连接关闭和重试的修复；不能长期复制一份官方代码后只添加指纹功能。跟踪公开修复和漏洞信息，说明每个候选是否受影响、是否需要回移以及验证结果。

## 12. 本阶段完成条件与会话交接

全部满足才可认为“替代 fhttp 的首版已完成”：

1. 官方/第三方来源固定，许可文本与修改记录随产物可获取；未确认来源的 fhttp 代码未进入实现。
2. 标准消费者和 fastls 类型迁移有编译证据；`chrome/156/windows` 等调用方式及已承诺的 SDK 行为保持一致。
3. H1/H2 的线上配置、64 MiB 流式、取消隔离、双工和资源回收经过独立验收。
4. 六组原生回归、C/Python 及适用的其他 SDK 消费者、race、长测通过；未执行/不适用项有明确原因。
5. 实际生产入口依赖闭包不含 fhttp，WS/H3/代理旁路也已经处理；没有通过隐藏代码路径或保留旧二进制绕过替换。
6. 性能结果可复现且差异公开，来源/源码/产物哈希对应同一候选；维护流程能继续同步上游。

给接手会话的起始任务：

> 在 ftransport 仓库阅读本文，核对相邻仓库分支和未提交改动。先执行 F0：落实合法来源与工具链决策，提出基于标准 HTTP 类型的最小接口，并实现有序 H1、H2 初始帧和 uTLS 连接交接三个可验证探针。随后按 F1–F4 推进，每个阶段提交实现、证据和剩余限制。不要覆盖其他会话改动，不要复制授权未明的 fhttp 实现，不要提前切换 fastls 默认后端或公开发布。阶段通过后，在独立集成分支完成核心及 FFI 的迁移与完整回归。

本次规划只完成仓库克隆、现有依赖接口核对、官方参考资料查阅及文档编写/检查；没有创建 go.mod、协议实现、功能测试或功能通过结论。后续执行以实际代码和 Actions 记录更新进度。
