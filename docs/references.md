# 参考资料

本文保存技术依据和相关项目调研。产品资料核对于 2026-09-29，语言、渲染、存储及打印接口资料核对于 2026-10-24；内容描述当时的文档和源码行为，固定提交链接用于复查。产品安装、设备兼容性与性能需要在实际环境验证。printkit 的当前实现见 [系统设计](design.md)。

## 服务与文档产物

### Go 与原生集成

Go 标准库的 `net/http` 提供 HTTP 服务，`os/exec` 管理外部进程，`database/sql` 提供事务与连接接口。SQLite 由独立驱动接入。这些能力覆盖 printkit 的接入、任务管理、持久化与浏览器进程协调。[HTTP](https://pkg.go.dev/net/http) · [进程](https://pkg.go.dev/os/exec) · [数据库](https://pkg.go.dev/database/sql)

Windows 可通过 `x/sys/windows` 加载系统 DLL 并调用函数，`x/sys/windows/svc` 支持 Windows Service。使用 cgo 连接 C 库时，交叉编译需要对应的 C 交叉编译器；结构布局、指针、回调和库生命周期需按具体接口处理。[DLL 调用源码](https://github.com/golang/sys/blob/master/windows/dll_windows.go) · [Windows Service](https://pkg.go.dev/golang.org/x/sys/windows/svc) · [cgo](https://pkg.go.dev/cmd/cgo)

语言比较记录的是实现成本和互操作条件：

| 语言 | 调研依据及与项目的关系 |
| --- | --- |
| Go | 标准库与现有组件适合当前任务服务；Go 1 提供语言与标准库兼容性承诺，`golang.org/x` 等子仓库的要求更宽松 |
| Rust | Microsoft 的 windows-rs 提供 C 风格、COM 和 WinRT 绑定，可用于评估复杂 Windows 后端；外部 C 库仍涉及 `unsafe`、ABI 和链接设置 |
| Zig | 调研基线仍包含语言和构建接口迁移；适合在具体底层组件需求出现时评估 |

Go 和 Rust 的语言兼容性承诺均需与第三方依赖版本管理配合。Rust 的 edition 迁移由项目选择。Zig 0.17.0 于 2026-10-01 发布，C 翻译由 `@cImport` 转向独立 `translate-c` 包，是该次评估工具链迁移成本的具体依据。[Go 兼容性](https://go.dev/doc/go1compat) · [windows-rs](https://github.com/microsoft/windows-rs) · [Rust FFI](https://doc.rust-lang.org/nomicon/ffi.html) · [Rust editions](https://doc.rust-lang.org/edition-guide/editions/index.html) · [Zig 0.17.0 发布说明](https://ziglang.org/download/0.17.0/release-notes.html#C-Translation-Moving-to-External-Package)

### 渲染、存储与 Web 接入

chromedp 通过 CDP 驱动独立浏览器，其 PDF 示例使用 `Page.printToPDF` 导出页面；协议提供背景、纸张、边距与 CSS 页面尺寸优先等设置。这为复用打印 CSS 提供实现路径，字体、资源和分页效果由业务样本验证。[chromedp](https://github.com/chromedp/chromedp) · [PDF 示例](https://github.com/chromedp/examples/blob/main/pdf/main.go) · [协议绑定源码](https://github.com/chromedp/cdproto/blob/main/page/page.go)

`modernc.org/sqlite` 是不依赖 cgo 的 `database/sql` 驱动，其文档列出 macOS、Windows 和 Linux 的受支持架构，并要求保持 `modernc.org/libc` 依赖版本匹配。SQLite 事务提供原子提交，适合将小文档与任务状态一起持久化。[驱动文档](https://pkg.go.dev/modernc.org/sqlite) · [SQLite 原子提交](https://www.sqlite.org/atomiccommit.html)

Chrome 的本地网络访问机制涉及网页到本机服务的权限。真实 HTTPS 业务来源需要按部署浏览器版本验证授权、CSP、跨域和拒绝授权时的交互。[Chrome 本地网络访问](https://developer.chrome.com/blog/local-network-access)

浏览器、字体、打印转换程序与驱动共同影响最终资源成本。低配置客户机的比较应使用相同文档、设备和驱动，并测量完整进程树。

## 系统打印接口

CUPS `lp` 默认输出系统任务编号；`lpstat` 默认只显示未结束任务，可用于初步排查。结构化 CUPS / IPP 接口提供 `job-state` 和 `job-state-reasons`。IPP 允许 `completed` 配合 `queued-in-device` 表示任务已交给不能继续报告状态的下游，状态模型需要保留反馈来源。[lp](https://apple.github.io/cups/doc/man-lp.html) · [lpstat](https://apple.github.io/cups/doc/man-lpstat.html) · [CUPS API](https://apple.github.io/cups/doc/cupspm.html) · [IPP 状态定义](https://www.rfc-editor.org/rfc/rfc8011.html#section-5.3.7)

Windows 的 RAW 数据需符合设备要求，通用 PDF 输入还需要转换。候选路径是 PDFium 渲染到打印机 HDC，配合 GDI `StartDocW` 取得系统 JobId，再查询队列。Sumatra CLI 提供静默打印和退出码，其公开文档未提供返回系统 JobId 的协议。[RAW 数据类型](https://learn.microsoft.com/en-us/windows-hardware/drivers/print/raw-data-type) · [StartDocW](https://learn.microsoft.com/en-us/windows/win32/api/wingdi/nf-wingdi-startdocw) · [PDFium 接口](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/public/fpdfview.h) · [Sumatra CLI](https://www.sumatrapdfreader.org/docs/Command-line-arguments)

## 相关项目

| 项目 | 定位 | 参考内容 |
| --- | --- | --- |
| LODOP / C-LODOP | Web 打印控件与打印服务 | HTML 接入、打印参数和状态查询 |
| PrintBridge + JS SDK | 本地打印 Agent 与 WebSocket 接入库 | HTML 渲染、后台运行、任务状态和多入口组织 |
| HttpPrinter | 本地打印、报表设计、打印浏览器与服务器 | 业务网页接入、单据能力和 Linux 分发 |
| PrintHub | 浏览器直连 Bluetooth / USB 热敏打印机的 JavaScript 库 | 小型票据接口和 ESC/POS 传输适配 |
| QZ Tray | 通过本地 WebSocket 提供打印能力 | 消息签名、授权与无界面运行配置 |

### LODOP / C-LODOP

官方将 C-LODOP 描述为 Windows Web 打印服务，也可部署成集中打印服务器；调研时 Lodop7 的下载项标为 Linux 桌面版。不同产品代际的平台范围应分别看待。[产品说明](https://www.lodop.net/) · [下载中心](https://www.lodop.net/download.html)

官方示例提供 `ADD_PRINT_HTM`、`ADD_PRINT_TABLE`、`PRINT` 和 `PREVIEW`，并演示通过 `innerHTML` 接入当前页面内容。这些接口覆盖 HTML、打印设置和预览；现代 CSS、字体、canvas、动态表单的保真范围仍需用业务单据验证。[接口与样例](https://www.lodop.net/LodopDemo_iframe.html) · [当前页面打印](https://www.lodop.net/demolist/PrintSample2.html)

状态示例区分加入后台队列和后续任务状态，提供 JOB 标识与查询。官方也说明设备状态存在差异，任务消失可能表示打印结束或被删除。该反馈区分为 printkit 的队列状态定义提供参考。[状态示例](https://www.lodop.net/demolist/PrintSample45.html)

既有材料未确认 macOS 本机执行、三平台开机未登录运行及资源成本。官方提供商业注册，具体产品与部署角色的授权、再分发条件尚未完整核对。[商业注册](https://www.lodop.net/Lodop_Lic.html)

### PrintBridge + JS SDK

源码基线：Agent 为 `e0f0b8c5365f38c300ac6accb7a322061dec2b69`，SDK 为 `c78844da3568c6fac0a82f011a94191b0a97b958`。源码链接固定到对应提交。

Agent 文档覆盖本地打印、三平台桌面端、CLI 运维和远程轮询，输入包括 PDF、图片、Office、原始指令及 HTML。SDK 通过 WebSocket 提供连接、单次与批量提交、打印机查询和状态事件，支持浏览器与 Node.js。[Agent README](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/README.md) · [SDK README](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md) · [SDK 实现](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/src/index.ts)

HTML 路径有源码依据：`html` 接收 URL，`raw-html` 接收字符串，Agent 使用本机已安装的 Chromium 系浏览器，以 headless 模式生成 PDF，可设置纸张与背景。文档限制 HTML 及资源访问公开 HTTP(S) 地址，因此业务内网、登录态与本机资源接入需要单独验证。[浏览器渲染](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/html/browser.rs#L554) · [技术说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/docs/printbridge-technical.md)

Linux 的 systemd 单元以 `printbridge` 系统用户运行 `print-bridge serve`，挂到 `multi-user.target`，提供了无人登录运行的实现依据。Windows Service 和 macOS LaunchDaemon 的对应能力未在既有调研中确认，Linux 开机打印也未实测。[systemd 单元](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/apps/server/packaging/systemd/print-bridge.service)

状态语义：SDK 的 `print()` 等待 `queued` 或错误；`submitted` 表示已提交系统队列，`completed` 表示系统或驱动层观察到结束，`unknown` 表示无法跟踪或超时。所读 Windows 后端经 SumatraPDF 提交，未获得系统任务 ID，跟踪返回 `Unknown`；CUPS 后端有任务查询逻辑。[SDK 状态说明](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md#状态监听) · [Windows 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/windows.rs#L120) · [CUPS 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/cups.rs)

SDK 序列化 `raw-html` 后交给 Agent，表单当前值、字体、资源与 canvas 的收集仍由接入方案处理。实现构造 `ws://host:port/ws`；HTTPS 页面、本地网络授权和企业浏览器策略尚待验证。浏览器渲染的整链路资源成本也没有比较基准。

主项目许可为 Apache-2.0，SDK 为 MIT；Windows 分发的 SumatraPDF 单列 GPLv3，需按组件核对分发条件。[Agent LICENSE](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/LICENSE) · [SDK LICENSE](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/LICENSE) · [第三方说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/THIRD_PARTY_NOTICES.md)

### HttpPrinter

官网列出网页与 Office/PDF 静默打印、拖拽报表设计、本地控件、打印浏览器和打印服务器等形态，可参考其业务单据接入和产品组织。[官网](https://www.httpprinter.com/)

官方 HTML 批量示例向本地 `/printreport` 发请求，另有 HTML 双面参数和 WebSocket PDF 示例。批量示例使用同步 Ajax，评估页面响应时需要区分调用代码与本地执行器的耗时。[HTML 批量](https://wiki.httpprinter.com/article/1780744713794) · [双面参数](https://wiki.httpprinter.com/article/1780744773957) · [PDF 静默打印](https://wiki.httpprinter.com/article/1780131178245)

当时的公开下载目录包含 Windows、Linux AMD64/ARM64 包和 Windows 打印浏览器包；知识库有银河麒麟运行说明。[下载页](https://www.httpprinter.com/file-browser.html) · [目录数据](https://www.httpprinter.com/api/directory-structure) · [麒麟说明](https://wiki.httpprinter.com/article/1781666631399)

macOS、本机无人登录生命周期、现代 CSS 保真和设备级完成反馈尚未确认，也没有资源成本实测。官网列有商业购买与源码版本方案，完整许可和再分发条款未核对。

### PrintHub（defuj/printhub）

仓库基线为 `47fca4b5ef0fc7e6efebc79799e859f422d991b6`，演示加载的发布包为 `printhub@1.2.1`。该仓库提交只有说明、演示页和静态资产，没有完整库源码目录、`package.json` 或独立许可文件；实现依据来自固定版本发布包。[仓库](https://github.com/defuj/printhub/tree/47fca4b5ef0fc7e6efebc79799e859f422d991b6) · [演示入口](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/index.html#L325)

README 描述面向 Bluetooth / USB 热敏打印机的 JavaScript 库，提供 58 / 80 毫米票据的文字、双列、图片、二维码和条码接口。发布代码通过 `navigator.bluetooth`、`navigator.usb` 连接设备，将 ESC/POS 字节经 GATT `writeValue()` 或 USB `transferOut()` 写入设备，适合参考票据接口与传输适配。[README](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md) · [1.2.1 发布实现](https://cdn.jsdelivr.net/npm/printhub@1.2.1/dist/index.js)

这条路径是浏览器设备直连，不经过 HTML/CSS 排版和系统打印队列。`onReady`、`onFailed` 对应连接结果，所读打印方法等待数据写入，未见设备完成通知链路；任务持久化、重试与执行历史仍待核实。[连接接口](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#connecting-to-printer-and-printing-text)

README 有浏览器兼容表和 Windows、Linux、macOS 的 USB 设置步骤，Windows 涉及 WinUSB 驱动。设备兼容性与各系统实际使用体验未做硬件测试。[USB 与浏览器说明](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#requirements-for-usb-printer)

许可信息有冲突：README 徽章写 MIT，正文、1.2.1 的 npm 元数据及包内 `package.json` 写 ISC；所读仓库和发布包均没有独立许可正文。采用代码前需查清对应版本的许可。[许可段落](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#license) · [版本元数据](https://registry.npmjs.org/printhub/1.2.1)

### QZ Tray

官方文档介绍本地 WebSocket 接入，支持 HTML、PDF、图片及原始指令；静默调用涉及消息签名，可用于参考浏览器与本机之间的授权设计。[入门说明](https://qz.io/docs/getting-started)

Headless 文档列出 Windows、Linux、macOS 命令，并要求预先配置证书白名单，以处理无界面环境的授权需求。既有调研未验证三平台开机未登录打印，也未展开性能、设备状态和许可比较。[Headless 文档](https://qz.io/docs/headless)
