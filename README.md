# ftransport

面向 fastls 的 Go HTTP 客户端传输库，计划提供可配置的 HTTP/1.1 与 HTTP/2 指纹、连接复用和有界流式传输，并优先兼容标准 `net/http` 类型。

**当前状态：F3 本地集成候选已完成。** 已实现可用于标准 `http.Client` 的并发安全 `Transport`、有序 H1 字段、显式 H2 SETTINGS/连接窗口/PRIORITY/HEADERS priority、伪头与普通头顺序、HPACK never-indexed、标准 TLS、自定义已握手连接交接、连接复用、流式、取消与关闭语义。`fastls` 与 FFI 的隔离分支已迁移到标准类型并移除 fhttp 编译闭包；正式发布、远端矩阵与性能基线属于 F4。

请先阅读 [实施与交接规划](IMPLEMENTATION_PLAN.md)。该文档定义架构边界、参考项目、接口草案、分阶段工作、迁移清单和测试门槛，可直接交给后续开发会话执行。

当前阶段材料：

- [设计与工具链决策](DESIGN.md)
- [固定来源与许可映射](SOURCES.md)
- [fastls 类型迁移清单](MIGRATION.md)
- [协议探针及复现方式](PROBES.md)

当前可以使用 `NewTransport(Config{})` 获得标准 H1/H2 传输。默认不读取环境代理、不自动解压响应，并使用标准证书验证。自定义拨号、协议指纹配置和请求级线上选项必须显式选择单一协议；自定义拨号调用方负责代理、TLS 握手和证书/主机名验证。定制 H2 支持每 origin 连接上限、空闲超时和完整关闭；独立的最大空闲连接计数尚不支持，非零配置会明确拒绝。

拟定模块路径：`github.com/FastTLS/ftransport`。

项目自有代码采用 Apache-2.0；官方 Go 代码及其他第三方材料保留原有许可和声明。当前材料不代表正式发布已经完成。
