# F0 协议探针

探针位于 `internal/probe`，只证明接口方向和线上字节可控，不是生产实现。

运行：

```text
go test -count=1 -v ./...
```

## 有序 HTTP/1.1

`TestOrderedH1ProbeThroughHTTPClient` 用真实 `http.Client` 和只用于测试的 RoundTripper 连接独立 TCP peer。peer 读取原始报文并逐字节核对：

```text
GET /probe?q=ordered HTTP/1.1
Host: <loopback address>
X-First: one
user-agent: ftransport-f0
X-Last: three
Connection: close
```

这证明标准 Client 可以保留 context/Response 语义，同时由自有 codec 决定字段顺序与大小写。

## HTTP/2 初始帧

`TestH2InitialFramesMatchStaticWireVector` 写出 client preface、一个有序 SETTINGS、连接 WINDOW_UPDATE 和 PRIORITY。SETTINGS 首项是 `INITIAL_WINDOW_SIZE=0`，用于证明显式零不会被默认值覆盖。整个输出与手工固定的十六进制向量比较，不用第二个同源编码器自证。

## uTLS ALPN 单连接交接

`TestUTLSALPNConnectionHandoffAndH2Reuse` 启动标准库 TLS/H2 peer，用测试根证书完成验证，以 uTLS 协商 `h2`，然后把同一个已经握手的 `net.Conn` 交给 HTTP/2 client connection。测试发出两个请求并核对：

- ALPN 实际结果为 `h2`；
- peer 收到两个 `HTTP/2.0` 请求；
- peer 只接受一个 TCP 连接；
- 两个请求的远端连接地址相同。

## 首次验证记录

2026-10-24 在 Windows amd64 上，Go 1.26.10 与 Go 1.27.3 的根包契约测试、三个探针和 `go vet` 全部通过。固定 `govulncheck v1.9.0` 报告 `No vulnerabilities found`。

F1 又加入标准 H1/H2、标准 TLS、自定义 TLS H1、uTLS H2、连接复用、trace、共享首拨取消隔离、H2 兄弟流取消、全双工、CloseIdle/完整 Close、阻塞上传源关闭、联网前拒绝请求体回收、错误分类、Stats 和 TLS 配置深拷贝测试。当时 Go 1.26.10 随机顺序全套重复 100 次通过；Go 1.27.3 随机顺序全套重复 50 次通过，根包语句覆盖率为 80.1%。

F2 新增生产路径 wire peer：H1 在同一连接逐行核对静态顺序和请求级覆盖；H2 独立解码有序 SETTINGS、WINDOW_UPDATE、初始 PRIORITY、HEADERS priority、伪头/普通头及 HPACK never-indexed，并单列显式空 SETTINGS 和 `INITIAL_WINDOW_SIZE=0`。受控 H2 peer 验证每 origin 连接上限、流槽释放后的复用和等待者独立取消。标准 TLS H2 完成 64 MiB 慢消费者摘要验收，定制快照路径继续通过兄弟流取消、全双工、uTLS 复用和关闭回收。`third_party/xnet/MANIFEST.sha256` 的 65 个本地文件哈希由测试逐项验证。

F2 完成时再次验证：Go 1.26.10 随机顺序全套重复 100 次通过；Go 1.27.3 随机顺序全套重复 50 次并通过 `go vet`；固定 `govulncheck v1.9.0` 报告 `No vulnerabilities found`。当前语句覆盖率为根包 81.1%、协议探针 75.5%。

本机默认 `CGO_ENABLED=0`；尝试以本机 MSVC 目标 clang 执行 race 时，runtime/cgo 不支持其 `-mthreads` 参数，因此本地 race 未验证。Linux CI 保留竞态门禁，并继续覆盖 Windows、Linux 和 macOS。
