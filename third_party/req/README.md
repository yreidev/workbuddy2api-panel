# req v3.61.0 固定源码

来源：<https://github.com/imroc/req/tree/v3.61.0>。Go module 校验值：

```
h1:JTfaJFkTFAfrG0r1mB6NkkAG+rx+DU8a0UctB0RR8yU=
```

这里包含该版本的完整运行源码、原 go.mod/go.sum 和 MIT LICENSE；不复制上游测试、文档及仓库工具。源码原有版权声明保留，本项目的改动采用根目录的 AGPL-3.0-only 与 NOTICE 条款。来源项目未认可或背书本改版。

相对上游只有以下行为改动（所有源码另加本仓库要求的文件头；`internal/chunked.go`、`internal/http3/client.go`、`internal/http3/conn.go` 另经 Go 1.26 gofmt 调整格式，`internal/http3/transport.go` 整理了 ABNF 注释的缩进）：

- `transport.go` 增加 `SetHTTP2PathWithoutIndex`，默认关闭，Clone 保留此设置。
- 每个 HTTP/2 连接按本次 SETTINGS 初始化响应报头列表上限，并发建连共享的 Transport 配置保持只读。
- `internal/http2/transport.go` 增加相应开关；启用时非静态 `:path` 不交给动态表编码器。
- `internal/http2/hpack_path.go` 仅编写 RFC 7541 的不索引 literal 和长度前缀，Huffman 使用现有 x/net/hpack。索引固定为官方 Chromium 样本中的静态名称 4。
- `SetHTTP2RespectHeaderTableSize` 默认关闭、Clone 保留；开启时跟随服务端 HEADER_TABLE_SIZE，包括超过默认 4096 的表。每次更新同时调整编码器容量上限与当前大小，避免 Go HPACK 默认上限截断服务端允许的扩容；官方样本已覆盖至 1 MiB。
- `internal/hpack` 保留 Go `golang.org/x/net v0.57.0/http2/hpack` 的五个运行源码文件和原 BSD 声明。原编码器增加默认关闭的 `IndexOversized`：Chromium 对超过表容量的字段仍使用 incremental indexing 并清空旧表，Go 原编码器会改用 without-indexing；`SetHTTP2IndexOversized` 单独控制该策略，默认关闭、Clone 保留。响应解码在 `ChromiumUpload` 路径使用这份副本的 `NewChromiumDecoder`，按官方样本区分索引、整数、Huffman、字符串长度、截断与动态表更新错误，并保留原解码器的动态表和 Huffman 实现；普通路径继续使用 x/net。
- `SetHTTP2ChromiumFlowControl` 默认关闭、Clone 保留；启用后按初始声明窗口的半窗与五秒消费间隔合并接收端 WINDOW_UPDATE。内部流额度同时改为使用实际发出的 SETTINGS_INITIAL_WINDOW_SIZE，避免声明 6 MiB 却仍只接受默认 4 MiB。原库未启用时继续使用原 4 KiB 更新策略。
- `SetHTTP2MaxUploadFrameSize` 默认零值、Clone 保留；三条 Trae profile 按官方样本限制 DATA 负载为 16375 字节，不改变 HEADERS 或服务端帧大小。读取正文时使用本次申请的缓冲长度，避免复用池中较大的缓冲后突破分片上限。
- `SetHTTP2ChromiumRetry` 默认关闭、Clone 保留；仅为 Work／Auth 开启。对明确的 REFUSED_STREAM 或排除当前流的 GOAWAY，同一外层请求最多立即重发两次。缓存连接的快速路径与后续拨号共用预算，取消或预算耗尽时不再创建回放正文；其他错误保留原重试策略。TTNet 不开启此设置，其正文没有回放源。
- 无法回放正文时，同时保留原 HTTP/2 拒绝／连接故障和回放错误，避免仅剩 `cannot rewind body` 而丢失上游原因；不改变是否允许回放的判断。
- `ChromiumUpload` 路径的接收端分别检查连接与流窗口：仅流超窗时发送该流的 `RST_STREAM(FLOW_CONTROL_ERROR)` 并归还违规片段的连接额度，其他流与连接保持；连接超窗仍返回连接级错误。未开启该路径时保留原库行为。官方样本与回归范围见[超窗记录](../../../docs/official-client.md#接收超窗的流与连接错误范围2026-11-18)。
- `chromium_frame.go` 仅为 `ChromiumUpload` 校准帧头校验、接收帧上限及 decoder 的 GOAWAY 错误码／调试文本；设置与 WINDOW_UPDATE 留给 session 按官方区分忽略、流错误及连接错误。普通 Framer 保留原路径。28 类官方矩阵、最终源码复采和具体边界见[异常帧记录](../../../docs/official-client.md#异常帧与发送窗口的关闭行为2026-11-18)。
- `read_headers.go` 在 Chromium 解码路径按已到达片段交付 HEADERS／CONTINUATION，确定的 HPACK 错误无需等待余下物理帧；合法片段仍等完整 payload、padding 与逻辑头块结束。未完整 Huffman 字符串复用原实现检查非法前缀，保留截断 EOF。连接空闲回收先禁止复用，等待已投递任务与控制帧排空后关闭；活动请求保持。依据与边界见[半帧记录](../../../docs/official-client.md#hpack-半帧的提前解码与错误2026-11-18)。
- Chromium 响应字段按已采官方聚合器校验，非法字段或头列表越限只重置单流并继续 HPACK 状态；合法额外伪字段与 trailers、信息响应及状态整数范围按各自处理阶段校准。读取错误先排入 RST 再通知请求，清理用同一标记去重。普通 req decoder 和响应路径保留原策略。[47 类官方矩阵及限制](../../../docs/official-client.md#响应头语义聚合限制与流错误2026-11-18)。
- Chromium 响应正文按实际 DATA／END_STREAM 交付，保留 Content-Length 元数据，移除这条路径继承的 Go 截断／提前结束长度检查；普通路径继续检查。`101` 按官方非 WebSocket 事务层转换为默认 200，后续头块仍作为 trailers；平台原生桥的零长度消费差异在网关 profile 中处理。[37 类生命周期矩阵与采样边界](../../../docs/official-client.md#信息响应结束与-content-length2026-11-18)。

路径和超大字段设置按官方样本为三条 Trae profile 开启。表大小设置只为已取得对应样本的 Work／Auth 开启；TTNet 本次收到大小变更后仍使用原表，未判作小表兼容通过。伪头顺序固定为 `:method,:authority,:scheme,:path`，因此编码器会在路径前写出待处理的表大小更新。连续请求、表大小变更及来源边界见 [HPACK 专项记录](../../../docs/upstream-req-hpack-alignment.md)。

HPACK 原源码：<https://cs.opensource.google/go/x/net/+/v0.57.0:http2/hpack/>。原模块校验值见根目录 `go.sum`，BSD 许可文本见 `GO-LICENSE`；上游并未认可本项目的改版。

根模块通过 replace 引用此目录，不能修改系统 Go 模块缓存代替这份补丁。版本升级时重新导入官方源码、复核本页差异，并运行网关的连续请求 HPACK 字节回归、表大小变化测试及传输测试。

原 Go 网络代码另采用 BSD 许可，其原声明保留，完整文本见 `GO-LICENSE`。原 req 的 MIT 文本见 `LICENSE`。发行时这些许可也随程序保存。
