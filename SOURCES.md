# 固定来源与许可映射

记录日期：2026-10-24。所有哈希均为完整 Git 对象 ID；模块校验值来自 Go checksum 数据。

## Go 标准库语义基线

- 项目：The Go Programming Language
- 来源：`https://go.googlesource.com/go`
- 标签：`go1.26.10`
- 提交：`4e8c85f5319d999bfd7617fdc191631e5634e3c5`
- 源码归档 SHA-256：`fd63e883ba2f2bfca93c1646b31ebef63e2b1e4d2b317c9789d976a68d81f36f`
- 许可：BSD-3-Clause
- 当前用途：HTTP 语义、公开类型、测试和维护基线；尚未复制标准库源文件。

同时验证的较新工具链：

- 标签：`go1.27.3`
- 提交：`2aae1000ea1d4b28923b96db28acf4e3b99d6aff`

## golang.org/x/net

- 来源：`https://go.googlesource.com/net`
- 模块：`golang.org/x/net`
- 标签：`v0.61.0`
- 提交：`c1d31df183e51b1075a2fd670d6f6f4efb871773`
- 模块校验值：`h1:RR+6j/BTBCrggOLugxa+66R8FvoN6MHlzDlZIbN9YFw=`
- go.mod 校验值：`h1:WgDCOTH8iwtB66T4/GfI94AHo/hhdb7Hah9NDseQfiI=`
- 许可：BSD-3-Clause 及附加专利授权，原文位于 `third_party/xnet/LICENSE` 与 `third_party/xnet/PATENTS`；汇总副本仍位于 `third_party/golang.org-x-net.LICENSE` 和 `third_party/golang.org-x-net.PATENTS`
- 当前用途：F0 探针、F1 中立传输，以及 F2 定制 HTTP/2 实现。生产副本位于 `internal/xnet`，避免依赖下游不会继承的 `replace`；`third_party/xnet` 保留逐文件上游来源。
- 快照范围：只包含当前构建闭包所需的 `http2`、`http2/hpack`、`http/httpguts`、`idna`、`internal/httpcommon` 与 `internal/httpsfv` 非测试源码。
- 逐文件来源和本地哈希：`third_party/xnet/MANIFEST.sha256`。修改目的与重放步骤：`third_party/xnet/README.ftransport.md`。
- 本地差异：Go 1.27+ 固定 legacy 客户端路径；增加有序/显式空 SETTINGS、接收窗口一致性、初始与 HEADERS priority、有序 HPACK 和敏感字段、显式零头列表限制，以及每 authority 连接上限与可独立取消的流槽等待。未复制 fhttp 源码。

2026-10-26：补齐 HTTP/2 preface/初始帧写入期间的拨号取消，防止已取消拨号继续占用连接池建连条目；生产副本、来源快照和本地摘要同步更新。上游原始摘要保留不变。

## uTLS

- 来源：`https://github.com/refraction-networking/utls`
- 模块：`github.com/refraction-networking/utls`
- 标签：`v1.8.2`
- 提交：`8fe0b08e9a0e7e2d08b268f451f2c79962e6acd0`
- 模块校验值：`h1:j4Q1gJj0xngdeH+Ox/qND11aEfhpgoEvV+S9iJ2IdQo=`
- go.mod 校验值：`h1:jkSOEkLqn+S/jtpEHPOsVv/4V4EVnelwbMQl4vCWXAM=`
- 许可：BSD-3-Clause，副本位于 `third_party/utls.LICENSE`
- 当前用途：用于 F0 探针和 F1 自定义连接交接测试。非测试实现不导入 uTLS，公共接口不绑定其具体类型。

## fhttp

`github.com/FastTLS/fhttp` 仅作为旧行为和迁移需求参考，不是本模块依赖。没有从其实现或测试复制源码。授权未确认的内容不得通过改名、重排或格式化进入本仓库。

## 本仓库代码

除带有单独上游声明的文件外，本仓库自有代码使用 Apache License 2.0，见根目录 `LICENSE`。第三方许可汇总见 `NOTICE` 和 `third_party/`。
