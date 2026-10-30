# ftransport

[English documentation](https://fasttls.github.io/en/ecosystem/ftransport)

面向 Go 标准 `net/http` 的 HTTP/1.1、HTTP/2 客户端传输库，用于配置头部顺序和 H2 连接参数。需要浏览器身份及 TLS ClientHello 配置时，使用上层 [Fastls](https://github.com/FastTLS/fastls)。

## 安装

Go 1.26+：

```sh
go get github.com/FastTLS/ftransport@v0.1.0
```

## 快速开始

```go
package main

import (
    "io"
    "log"
    "net/http"
    "time"
    "github.com/FastTLS/ftransport"
)

func main() {
    if err := run(); err != nil { log.Fatal(err) }
}

func run() error {
    transport, err := ftransport.NewTransport(ftransport.Config{})
    if err != nil { return err }
    defer transport.Close()
    client := &http.Client{Transport: transport, Timeout: 30*time.Second}
    response, err := client.Get("https://example.com/")
    if err != nil { return err }
    defer response.Body.Close()
    _, err = io.Copy(io.Discard, response.Body)
    return err
}
```

## 支持与限制

### H2 流级配置

`HTTP2Config.FirstStreamID` 为每个物理连接的首个请求流编号；`0` 或省略选择 `1`，其它值须为不超过 `2^31-1` 的奇数，后续请求每次增加 2。不会发送占位请求；编号耗尽后换连接。

`StreamWindowIncrement` 在每个请求的初始 HEADERS（含全部 CONTINUATION）之后发送一次流 WINDOW_UPDATE，增加相同数量的实际接收额度；不会因请求 Trailer 再发一次。`0` 不发送。与 SETTINGS_INITIAL_WINDOW_SIZE 的和不得超过 `2^31-1`，配置会在拨号前校验。增加额度也增加单流可能缓冲的响应数据量，调用方应结合并发上限选择预算。

配置指纹参数须显式选择 `Protocols: []ftransport.Protocol{ftransport.ProtocolHTTP2}`。可选 `ftransport_http2_stdwrap` 后端不支持这两项，并明确拒绝配置。默认值与未配置的旧请求行为保持兼容。

- 并发安全的 RoundTripper，H1/H2 连接复用、流式读取、取消和关闭。
- H1 有序头；H2 显式有序/空 SETTINGS、连接窗口、PRIORITY、HEADERS priority、伪头/普通头顺序和 HPACK never-indexed。
- 默认标准 TLS 验证；默认不读取环境代理，也不自动解压响应。
- 自定义拨号和协议指纹配置须显式选择单一协议。自定义拨号方负责代理、TLS 握手、证书与主机名验证；交接成功后连接归传输所有。
- 本库不提供浏览器配置生成、JavaScript 执行或 HTTP/3。标准 net/http 类型兼容不等于与其所有内部行为完全相同。
- 定制 H2 支持每 origin 连接上限、空闲超时及完整关闭；独立最大空闲连接计数尚不支持，非零配置会拒绝。

开发分支另包含 H2 preface / 初始帧写入阶段的有界取消、失败上传源关闭及后续等待者恢复。验证见 `dial_cancellation_test.go`；该改动尚未进入正式标签。

## 进阶与维护

[设计与配置](DESIGN.md) · [协议探针](PROBES.md) · [迁移](MIGRATION.md) · [来源与许可证映射](SOURCES.md)。[实施规划](IMPLEMENTATION_PLAN.md) 是历史维护资料，实际发布以标签为准。

## 详细文档与问题反馈

- [使用文档](https://fasttls.github.io/ecosystem/)：安装、功能、参数与排错。
- [版本与发布说明](https://github.com/FastTLS/ftransport/releases)：正式版本、开发版本和产物边界。
- [问题反馈](https://github.com/FastTLS/ftransport/issues)：请提供版本、系统架构、脱敏后的最小示例与错误；不要提交凭据、Cookie、私钥或个人数据。

## 开源协议与第三方声明

项目自有代码采用 [Apache-2.0](LICENSE)，见 [NOTICE](NOTICE)。Go 与 x/net 等第三方来源保留 BSD 及专利声明，详见 [SOURCES.md](SOURCES.md)。

## 使用边界与免责声明

仅在拥有权限的系统、数据和网络上使用，并遵守适用法律、服务条款及隐私要求。软件按许可证约定以“现状”提供，不承诺可用性、特定用途适用性、第三方服务放行、匿名性或与真实浏览器完全一致。浏览器名称和版本用于描述配置，不表示获得相应厂商认证或背书。部署方负责访问控制、密钥和个人信息保护、请求限额及数据备份；担保排除与责任限制以随附许可证及适用法律为准。本说明不改变许可证授予的权利或新增许可限制。
