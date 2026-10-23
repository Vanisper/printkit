# 参考项目

以下产品资料来自 2026-09-29 的调研，依据官方文档、公开源码及发布清单，未安装产品、连接打印机或执行性能测试，也未重新核实产品后续版本。文末的技术选型依据于 2026-10-24 单独核对。

| 项目 | 定位 | 主要参考价值 |
| --- | --- | --- |
| LODOP / C-LODOP | Web 打印控件与打印服务 | 现有 HTML 接入、打印参数和状态查询 |
| PrintBridge + JS SDK | 本地打印 Agent 与 WebSocket 接入库 | HTML 渲染、后台运行、任务状态和多入口组织 |
| HttpPrinter | 本地打印、报表设计、打印浏览器与服务器等产品形态 | 业务网页接入、单据能力和 Linux 分发 |
| PrintHub | 浏览器直连 Bluetooth / USB 热敏打印机的 JavaScript 库 | 小型票据接口和 ESC/POS 传输适配 |
| QZ Tray | 通过本地 WebSocket 提供打印能力 | 消息签名、授权与无界面运行配置 |

## LODOP / C-LODOP

官方将 C-LODOP 描述为 Windows Web 打印服务，也可部署成集中打印服务器；调研时 Lodop7 的下载项标为 Linux 桌面版。不同产品代际的平台范围应分别看待。[产品说明](https://www.lodop.net/) · [下载中心](https://www.lodop.net/download.html)

官方示例提供 `ADD_PRINT_HTM`、`ADD_PRINT_TABLE`、`PRINT` 和 `PREVIEW`，并演示通过 `innerHTML` 接入当前页面内容。值得参考其 HTML、打印设置和预览接口；现代 CSS、字体、canvas、动态表单的保真范围仍需用业务单据验证。[接口与样例](https://www.lodop.net/LodopDemo_iframe.html) · [当前页面打印](https://www.lodop.net/demolist/PrintSample2.html)

状态示例区分加入后台队列和后续任务状态，提供 JOB 标识与查询。官方也说明设备状态存在差异，任务消失可能表示打印结束或被删除。这个边界适合用于设计 printkit 的状态含义。[状态示例](https://www.lodop.net/demolist/PrintSample45.html)

既有材料未确认 macOS 本机执行、三平台开机未登录运行及资源成本。官方提供商业注册，具体产品与部署角色的授权、再分发条件尚未完整核对。[商业注册](https://www.lodop.net/Lodop_Lic.html)

## PrintBridge + JS SDK

源码基线：Agent 为 `e0f0b8c5365f38c300ac6accb7a322061dec2b69`，SDK 为 `c78844da3568c6fac0a82f011a94191b0a97b958`。以下链接固定到这两个提交。

Agent 文档覆盖本地打印、三平台桌面端、CLI 运维和远程轮询，输入包括 PDF、图片、Office、原始指令及 HTML。SDK 通过 WebSocket 提供连接、单次与批量提交、打印机查询和状态事件，支持浏览器与 Node.js。[Agent README](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/README.md) · [SDK README](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md) · [SDK 实现](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/src/index.ts)

HTML 路径有源码依据：`html` 接收 URL，`raw-html` 接收字符串，Agent 使用本机已安装的 Chromium 系浏览器，以 headless 模式生成 PDF，可设置纸张与背景。文档限制 HTML 及资源访问公开 HTTP(S) 地址，因此业务内网、登录态与本机资源接入需要单独验证。[浏览器渲染](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/html/browser.rs#L554) · [技术说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/docs/printbridge-technical.md)

Linux 的 systemd 单元以 `printbridge` 系统用户运行 `print-bridge serve`，挂到 `multi-user.target`，提供了无人登录运行的实现依据。Windows Service 和 macOS LaunchDaemon 的对应能力未在既有调研中确认，Linux 开机打印也未实测。[systemd 单元](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/apps/server/packaging/systemd/print-bridge.service)

状态设计值得参考：SDK 的 `print()` 等待 `queued` 或错误；`submitted` 表示已提交系统队列，`completed` 表示系统或驱动层观察到结束，`unknown` 表示无法跟踪或超时。所读 Windows 后端经 SumatraPDF 提交，未获得系统任务 ID，跟踪返回 `Unknown`；CUPS 后端有任务查询逻辑。[SDK 状态说明](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md#状态监听) · [Windows 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/windows.rs#L120) · [CUPS 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/cups.rs)

SDK 序列化 `raw-html` 后交给 Agent，表单当前值、字体、资源与 canvas 的收集仍由接入方案处理。实现构造 `ws://host:port/ws`；HTTPS 页面、本地网络授权和企业浏览器策略尚待验证。浏览器渲染的整链路资源成本也没有比较基准。

主项目许可为 Apache-2.0，SDK 为 MIT；Windows 分发的 SumatraPDF 单列 GPLv3，需按组件核对分发条件。[Agent LICENSE](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/LICENSE) · [SDK LICENSE](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/LICENSE) · [第三方说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/THIRD_PARTY_NOTICES.md)

## HttpPrinter

官网列出网页与 Office/PDF 静默打印、拖拽报表设计、本地控件、打印浏览器和打印服务器等形态，可参考其业务单据接入和产品组织。[官网](https://www.httpprinter.com/)

官方 HTML 批量示例向本地 `/printreport` 发请求，另有 HTML 双面参数和 WebSocket PDF 示例。批量示例使用同步 Ajax，评估页面响应时需要区分调用代码与本地执行器的耗时。[HTML 批量](https://wiki.httpprinter.com/article/1780744713794) · [双面参数](https://wiki.httpprinter.com/article/1780744773957) · [PDF 静默打印](https://wiki.httpprinter.com/article/1780131178245)

当时的公开下载目录包含 Windows、Linux AMD64/ARM64 包和 Windows 打印浏览器包；知识库有银河麒麟运行说明。[下载页](https://www.httpprinter.com/file-browser.html) · [目录数据](https://www.httpprinter.com/api/directory-structure) · [麒麟说明](https://wiki.httpprinter.com/article/1781666631399)

macOS、本机无人登录生命周期、现代 CSS 保真和设备级完成反馈尚未确认，也没有资源成本实测。官网列有商业购买与源码版本方案，完整许可和再分发条款未核对。

## PrintHub（defuj/printhub）

仓库基线为 `47fca4b5ef0fc7e6efebc79799e859f422d991b6`，演示加载的发布包为 `printhub@1.2.1`。该仓库提交只有说明、演示页和静态资产，没有完整库源码目录、`package.json` 或独立许可文件；实现依据来自固定版本发布包。[仓库](https://github.com/defuj/printhub/tree/47fca4b5ef0fc7e6efebc79799e859f422d991b6) · [演示入口](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/index.html#L325)

README 描述面向 Bluetooth / USB 热敏打印机的 JavaScript 库，提供 58 / 80 毫米票据的文字、双列、图片、二维码和条码接口。发布代码通过 `navigator.bluetooth`、`navigator.usb` 连接设备，将 ESC/POS 字节经 GATT `writeValue()` 或 USB `transferOut()` 写入设备，适合参考票据接口与传输适配。[README](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md) · [1.2.1 发布实现](https://cdn.jsdelivr.net/npm/printhub@1.2.1/dist/index.js)

这条路径是浏览器设备直连，不经过 HTML/CSS 排版和系统打印队列。`onReady`、`onFailed` 对应连接结果，所读打印方法等待数据写入，未见设备完成通知链路；任务持久化、重试与执行历史仍待核实。[连接接口](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#connecting-to-printer-and-printing-text)

README 有浏览器兼容表和 Windows、Linux、macOS 的 USB 设置步骤，Windows 涉及 WinUSB 驱动。设备兼容性与各系统实际使用体验未做硬件测试。[USB 与浏览器说明](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#requirements-for-usb-printer)

许可信息有冲突：README 徽章写 MIT，正文、1.2.1 的 npm 元数据及包内 `package.json` 写 ISC；所读仓库和发布包均没有独立许可正文。采用代码前需查清对应版本的许可。[许可段落](https://github.com/defuj/printhub/blob/47fca4b5ef0fc7e6efebc79799e859f422d991b6/readme.md#license) · [版本元数据](https://registry.npmjs.org/printhub/1.2.1)

## QZ Tray

官方文档介绍本地 WebSocket 接入，支持 HTML、PDF、图片及原始指令；静默调用涉及消息签名，可用于参考浏览器与本机之间的授权设计。[入门说明](https://qz.io/docs/getting-started)

Headless 文档列出 Windows、Linux、macOS 命令，并要求预先配置证书白名单，以处理无界面环境的授权需求。既有调研未验证三平台开机未登录打印，也未展开性能、设备状态和许可比较。[Headless 文档](https://qz.io/docs/headless)

## 用于后续验证

现有方案已提供 HTML 渲染、本地代理、静默提交和状态查询等能力。printkit 应围绕自己的业务样本验证接入成本与运行表现：

- 用双联单和汇总单检查动态字段、字体、资源、分页及背景的还原程度
- 分别测试桌面登录、锁屏、注销和开机未登录时的任务执行
- 区分任务接收、系统提交、系统报告结束和物理出纸，验证结果未知时的恢复与补打
- 在相同文档、设备和驱动下测量完整进程树的资源占用与耗时

## 技术选型依据（2026-10-24）

本节重新核对语言及工具的官方文档、维护者文档和源码，未运行语言性能对比。当前没有可用打印机，先在 macOS 开发机验证提交、持久化、HTML 转 PDF、查询和恢复；文件输出只证明产物生成，设备输出仍需后续验收。

### Go、Rust 与 Zig

下面记录事实依据，结合项目作出的选择见 [设计文档](design.md)。

**Go 的现成能力。** `net/http` 提供服务端，`os/exec` 管理外部进程，`database/sql` 提供事务与连接接口，但不自带 SQLite 驱动。chromedp 通过 CDP 驱动浏览器，不需要额外的 WebDriver；浏览器程序仍是独立依赖。[HTTP](https://pkg.go.dev/net/http) · [进程](https://pkg.go.dev/os/exec) · [数据库](https://pkg.go.dev/database/sql) · [chromedp](https://github.com/chromedp/chromedp)

**Go 的原生边界。** Windows 可通过 `x/sys/windows` 加载系统 DLL 并调用函数，Windows Service 也有 `x/sys/windows/svc` 支持，因此调用原生 API 不必然引入 cgo。若后端使用 cgo 连接 C 库，交叉编译需要对应的 C 交叉编译器；结构布局、指针、回调和库生命周期仍需逐项处理。[DLL 调用源码](https://github.com/golang/sys/blob/master/windows/dll_windows.go) · [Windows Service](https://pkg.go.dev/golang.org/x/sys/windows/svc) · [cgo](https://pkg.go.dev/cmd/cgo)

**Rust 的实际优势。** Microsoft 的 windows-rs 提供 C 风格、COM 和 WinRT 绑定，也提供服务相关组件，可减少自行声明 Windows 接口的工作。它并不替代 PDF 引擎或打印驱动；调用外部 C 库仍涉及 `unsafe`、ABI 和链接设置。[windows-rs](https://github.com/microsoft/windows-rs) · [Rust FFI](https://doc.rust-lang.org/nomicon/ffi.html)

**稳定性要区分语言与依赖。** Go 1 对语言和标准库提供兼容性承诺，`golang.org/x` 等子仓库要求更宽松。Rust 的稳定语言功能持续支持，edition 迁移由项目选择。两者的兼容性承诺都不能代替第三方库版本管理。[Go 兼容性](https://go.dev/doc/go1compat) · [Rust editions](https://doc.rust-lang.org/edition-guide/editions/index.html)

**Zig 需要按当前版本评估。** 查询时官方稳定发布为 0.17.0，发布时间为 2026-10-01；发布说明仍在推进语言稳定，并包含语言和构建系统迁移。例如 `@cImport` 已移除，C 翻译改用 ZSF 的独立 `translate-c` 包。其原生互操作能力成立，但当前项目需额外承担工具链变动，选择它不能只依据旧版示例或二进制体积印象。[下载页](https://ziglang.org/download/) · [0.17.0 发布说明](https://ziglang.org/download/0.17.0/release-notes.html#C-Translation-Moving-to-External-Package)

### 对原型和后续实现的影响

选型判断是先用 Go 完成任务服务，固定 Go、chromedp、SQLite 驱动和浏览器的版本。首期围绕文件产物验证软件链路，将真实打印限制在独立后端内；完整实现方案见 [设计文档](design.md)。

- 主服务采用不依赖 cgo 的组件，可以简化这一部分的构建；完整交付仍需浏览器，真实打印阶段还会涉及平台组件、驱动和字体。
- 如果后续核心工作转为大量 Windows COM / WinRT 或 PDF 原生库集成，再用一个真实后端验证 Rust 是否减少绑定和维护工作；先比较后端实现，无需预先安排整体重写。
- Zig 适合在出现明确的底层组件或 C 工具链需求时单独评估；当前没有足够收益支持为主服务承担迁移成本。
- 语言不会消除渲染与打印依赖。低配置机器的收益需测量主服务、Chromium 和打印转换进程的总资源占用，不能由 Go、Rust 或 Zig 的一般性能结论推定。

### 渲染、存储与 Web 接入

chromedp 的 PDF 示例通过 CDP 的 `Page.printToPDF` 导出页面；协议提供背景、纸张与 CSS 页面尺寸优先等设置。这支持先复用打印 CSS 的方案，业务字体、资源和分页仍需样本验证。[PDF 示例](https://github.com/chromedp/examples/blob/main/pdf/main.go) · [协议绑定源码](https://github.com/chromedp/cdproto/blob/main/page/page.go)

`modernc.org/sqlite` 是不依赖 cgo 的 `database/sql` 驱动，文档列出 macOS、Windows 和 Linux 的受支持架构，并要求保持其 `modernc.org/libc` 依赖版本匹配。SQLite 事务提供原子提交；原型将小文档与状态放入同一数据库，是为了简化一致性处理，未据此主张存储性能更优。[驱动文档](https://pkg.go.dev/modernc.org/sqlite) · [SQLite 原子提交](https://www.sqlite.org/atomiccommit.html)

Chrome 的本地网络访问机制涉及网页到本机服务的权限。HTTP 轮询能简化原型接口，但真实 HTTPS 来源仍需按浏览器版本核对授权、CSP 和跨域行为；原型保留这项独立验收。[Chrome 本地网络访问](https://developer.chrome.com/blog/local-network-access)

### 后续打印后端

CUPS `lp` 默认输出系统任务编号；`lpstat` 默认只显示未结束任务，适合初步排查。需要明确状态时，使用结构化 CUPS / IPP 接口获取 `job-state` 和 `job-state-reasons`。IPP 允许 `completed` 配合 `queued-in-device` 表示交给不能继续报告状态的下游，因此仍需保留反馈来源。[lp](https://apple.github.io/cups/doc/man-lp.html) · [lpstat](https://apple.github.io/cups/doc/man-lpstat.html) · [CUPS API](https://apple.github.io/cups/doc/cupspm.html) · [IPP 状态定义](https://www.rfc-editor.org/rfc/rfc8011.html#section-5.3.7)

Windows 的 RAW 数据需符合设备要求，通用 PDF 输入还需要转换。可验证的候选路径是 PDFium 渲染到打印机 HDC，配合 GDI `StartDocW` 取得系统 JobId，再查询队列。Sumatra CLI 提供静默打印和退出码，但其公开文档没有返回系统 JobId 的协议，适合出纸试验，不能据此建立完整任务状态。[RAW 数据类型](https://learn.microsoft.com/en-us/windows-hardware/drivers/print/raw-data-type) · [StartDocW](https://learn.microsoft.com/en-us/windows/win32/api/wingdi/nf-wingdi-startdocw) · [PDFium 接口](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/public/fpdfview.h) · [Sumatra CLI](https://www.sumatrapdfreader.org/docs/Command-line-arguments)
