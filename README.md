# ftransport

面向 fastls 的 Go HTTP 客户端传输库，提供可配置的 HTTP/1.1 与 HTTP/2 指纹、连接复用和有界流式传输，并优先兼容标准 `net/http` 类型。

**当前正式版：[`v0.1.0`](https://github.com/FastTLS/ftransport/releases/tag/v0.1.0)。** 已实现可用于标准 `http.Client` 的并发安全 `Transport`、有序 H1 字段、显式 H2 SETTINGS/连接窗口/PRIORITY/HEADERS priority、伪头与普通头顺序、HPACK never-indexed、标准 TLS、自定义已握手连接交接、连接复用、流式、取消与关闭语义。`fastls v0.1.1` 和 `fastls-ffi v0.1.2` 已使用该正式版，生产编译闭包不包含 fhttp。

[实施与交接规划](IMPLEMENTATION_PLAN.md)保留首版设计、来源和验收历史；当前使用方式及边界以本文和公开 API 为准。

当前阶段材料：

- [设计与工具链决策](DESIGN.md)
- [固定来源与许可映射](SOURCES.md)
- [fastls 类型迁移清单](MIGRATION.md)
- [协议探针及复现方式](PROBES.md)

当前可以使用 `NewTransport(Config{})` 获得标准 H1/H2 传输。默认不读取环境代理、不自动解压响应，并使用标准证书验证。自定义拨号、协议指纹配置和请求级线上选项必须显式选择单一协议；自定义拨号调用方负责代理、TLS 握手和证书/主机名验证。定制 H2 支持每 origin 连接上限、空闲超时和完整关闭；独立的最大空闲连接计数尚不支持，非零配置会明确拒绝。

2026-10-26 开发更新：HTTP/2 拨号取消现覆盖 preface 和初始帧写入；建连失败会关闭上传源并释放活动计数，有效等待者可恢复建连。该修复尚未进入正式标签，验证用例见 `dial_cancellation_test.go`；快照、生产副本和来源摘要同步维护。

模块路径：`github.com/FastTLS/ftransport`。

项目自有代码采用 Apache-2.0；官方 Go 代码及其他第三方材料保留原有许可和声明。正式源码、标签和版本说明仅在 GitHub 发布。
