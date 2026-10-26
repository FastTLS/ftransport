# fastls 到 ftransport 的类型迁移清单

归档状态：迁移及 F4 发布验证已完成。`ftransport v0.1.0`、`fastls v0.1.1` 和 `fastls-ffi v0.1.2` 已正式发布；下文保留迁移时的工作树和验证记录。

## 已核对基线

- `fastls` 基线：分支 `v2`，提交 `3c19646b30097cb1d74962ba3df7cd2a99130a1a`；集成工作树为 `fastls-ftransport-f3`。
- `fhttp`：分支 `v2`，提交 `e027ce786984d3e7e831b0d10c54875dec746b35`；`http2/h2demo/go.mod` 与 `go.sum` 有既存未提交改动，不得触碰。
- `fastls-ffi-next-phase` 基线：分支 `next-phase-distribution`，提交 `8ced37557f45fff039e850f75ab8c036905e562d`；集成工作树为 `fastls-ffi-ftransport-f3`。
- `fastls-ffi` main 有大量既存未提交改动，集成必须使用独立 worktree 或等待其归属工作完成。
- `imitate`：分支 `v2`，提交 `28b32d452256e3acbdb2e904869e5c4331bf0177`，工作树干净。
- uTLS 本地仓库：分支 `codex/fastls-custom`，标签 `v1.8.2`，提交 `8fe0b08e9a0e7e2d08b268f451f2c79962e6acd0`。

规划文档中的 `../fastls` 等路径以 `ftransport` 的父目录为参照不正确。当前真实根目录是 `D:/Projects/fastls/`，相邻仓库位于其下。实现和脚本不得继续使用旧相对假设。

## 公共类型映射

- `fhttp.Client` 改为 `net/http.Client`。fastls 的 `Response.Client` 和内部 `requestContext.client` 会发生公开 Go 类型变化，必须发布迁移说明和消费者编译测试。
- `fhttp.Request`、`fhttp.Response`、`fhttp.Header`、`fhttp.Cookie` 和 `fhttp.CookieJar` 分别改为标准库对应类型。
- `fhttp/cookiejar` 改为 `net/http/cookiejar`，保留 publicsuffix、显式 Cookie 覆盖、Secure、Path、Domain 和过期行为测试。
- fhttp 的特殊 `HeaderOrderKey` 与 pseudo-header key 不再伪装成真实 Header。fastls 把 `HeaderOrderKeys`、`PHeaderOrderKeys` 转为 `ftransport.RequestOptions`。
- `http2.HTTP2Settings.Explicit` 转为 `HTTP2Settings.Present`；空但 Present 的列表必须发送空 SETTINGS。
- fhttp `Setting{ID, Val}` 转为 `HTTP2Setting{ID, Value}`，保留列表顺序和显式零。
- `ConnectionFlow` 转为 `ConnectionWindowIncrement`，语义保持为初始 65535 之外的 WINDOW_UPDATE 增量。
- fhttp priority 的线上 0..255 权重不能直接泄露到公共 API。profile 的 1..256 权重映射到 `HTTP2PriorityParam.Weight`，编码时减一。
- `PHeaderOrderKeys` 转为受校验的 `[]PseudoHeader`。

## 文件级迁移

- `client.go` 与 `index.go`：替换导入、公共 Client/CookieJar/HTTP2 配置类型和 redirect hook。
- `session.go`：使用标准 Client/Jar；保持会话隔离、请求 Cookie 覆盖和 CloseIdle 行为。
- `profile_execution.go`：把 profile 的有序 setting、窗口增量、header priority 和 priority frames 转成 ftransport 配置，保留 Explicit 语义。
- `h2settings.go`：旧字符串 parser 可以保留在 fastls 兼容层，但输出新配置；不得把 parser 默认值混入显式列表。
- `roundtripper.go`：最终由 ftransport Transport 取代 H1/H2、协议探测和旧类型转换；ALPN、证书验证、拨号身份和池隔离逐项验收。
- `timeouts.go`：切换到标准 `net/http/httptrace` 和 ftransport 的阶段事件；不使用共享连接 deadline 取消单个 H2 流。
- `cookie_store.go`：迁移到标准 Cookie/Jar 类型并保留现有域、路径、Secure、过期和公共后缀测试。
- `connect.go` 与 `proxy_transport.go`：H2 CONNECT 是否继续使用独立官方适配需要靶场确认；目标与代理 CA、认证隔离、SOCKS DNS 模式不能退化。
- `http3_transport.go`：移除 fhttp 类型转换，即使 H3 不由 ftransport 首版实现，也不能让生产依赖闭包保留 fhttp。
- `websocket_context.go`：保持当前独立标准 HTTP/Gorilla/uTLS 路径；本轮不把 WS 协议搬入 ftransport。
- FFI `internal/bridge/cookies.go` 和 `websocket.go`：迁到标准类型，保持 C ABI、JSON 协议和 SDK 行为。

## 工具链迁移

fastls 当前声明 Go 1.24 和 toolchain 1.24.11。集成 ftransport 主线前必须更新到 Go 1.26.10，并同步 CI、go.work、FFI 构建镜像和发行文档。此变化是安全基线决定，不宣称对旧 Go 源码兼容。

## 最终闭包检查

本地真实入口的 `go list -deps` 已证明核心与 FFI Go 编译闭包没有 fhttp；H3、WS、代理及现有 Go 测试通过。完整 c-shared/Python/wheel 本机复验需要 MinGW-w64 或等价 GNU Windows sysroot，远端多平台矩阵在 F4 固定提交后执行。
