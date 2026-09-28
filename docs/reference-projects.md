# 参考项目核实

核实日期：2026-09-29。

本文用于建立 PrintKit 的比较基线，不是产品排名，也不证明 PrintKit 已经实现或优于任何方案。
本轮仅阅读官方站点、项目文档、公开源码与安装清单，未安装产品、连接打印机或执行性能测试。

## 阅读规则

- **项目自述**：官方文档描述的能力，尚未通过本项目的设备和场景验证。
- **代码证据**：在指定源码版本看到相关实现，只证明实现路径存在，不证明所有部署环境都可用。
- **待验证**：材料不足或尚未实测；不能据此判定对方不支持。

PrintKit 当前定位是独立、通用的本地打印系统。Web bridge、桌面 UI、CLI/SDK 和远程连接器都是入口；中央任务服务器与模板设计器不作为 MVP 的前提。Go、Rust、Zig 等技术选型仍待验证。

## 概览

| 参考对象 | 已查明的接入方式 | 平台与运行方式证据 | 比较时应保留的边界 |
| --- | --- | --- | --- |
| Lodop / C-Lodop | JS 打印指令、HTML 内容、URL、设计与预览 | 官方列出 Windows C-Lodop、Linux 桌面 Lodop7 | 产品代际要分开；三平台无人登录运行待验证 |
| PrintBridge | WebSocket、本机 Agent、CLI 运维、远程轮询 | 自述三平台 Desktop；Linux 有 systemd Headless 实现 | 已具备 HTML 转 PDF；不能当作单纯文件转发器 |
| print-bridge-jssdk | 浏览器或 Node.js 经 WebSocket 提交任务 | SDK 依赖独立 Agent | SDK 不承担本机渲染、系统服务或驱动职责 |
| HttpPrinter | 本地接口、网页静默打印、报表设计、打印浏览器等 | 下载目录有 Windows、Linux 包 | macOS、无人登录和状态精确定义待验证 |

表中事实的具体来源、版本和限制见下文，不能将“运行于三平台”与“三平台无人登录自动运行”视为同一个结论。

## Lodop / C-Lodop

官方将 C-Lodop 描述为 Windows Web 打印服务，也可独立部署成集中打印服务器；Lodop7 的当前下载项明确标为 Linux 桌面版。[产品说明](https://www.lodop.net/) · [下载中心](https://www.lodop.net/download.html)

**HTML 与静默接入：项目自述及官方示例。**接口包含 `ADD_PRINT_HTM`、`ADD_PRINT_TABLE`、直接打印 `PRINT` 和预览 `PREVIEW`。当前页面示例通过 `innerHTML` 把目标内容送入控件，并非只能从专有设计器创建内容。[接口与样例目录](https://www.lodop.net/LodopDemo_iframe.html) · [当前页面打印示例](https://www.lodop.net/demolist/PrintSample2.html)

现有 HTML 能够作为输入，不代表任意现代 CSS、`@media print`、字体、canvas 和动态状态均与业务浏览器一致；本轮没有执行兼容性测试。

**状态：官方已有边界说明。**样例 45 区分“成功加入后台队列”和后续任务状态，并提供 JOB 标识及状态查询；同时承认设备状态差异，也说明任务消失可能是打印完毕或被删除。[状态示例](https://www.lodop.net/demolist/PrintSample45.html)

因此不能把该接口名称中的“成功”直接解释为所有打印机都已可靠确认出纸，也不能说它没有状态跟踪能力。

**运行方式与成本：待验证。**本轮未获得足够证据确认 macOS 本机执行及三平台开机未登录模式；“Web 打印服务”这一产品名称本身不足以确认系统服务生命周期。安装包体积宣传也不能替代运行内存、CPU 和长任务基准。

官方提供商业注册入口；本轮未完整核对各产品、版本和部署角色的授权条款，不据此给出复用或再分发结论。[商业注册说明](https://www.lodop.net/Lodop_Lic.html)

## PrintBridge

源码基线：`e0f0b8c5365f38c300ac6accb7a322061dec2b69`。以下源码链接固定到该提交，后续版本可能变化。

**定位与能力：项目自述。**当前项目已覆盖本地 Agent、三平台桌面端、CLI 运维、远程轮询、PDF/图片/Office/原始指令及 HTML；不要求所有任务都来自远程中心。[README](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/README.md)

**HTML：存在代码证据。**`html` 接收 URL，`raw-html` 接收字符串；本机寻找已安装的 Chromium 系浏览器，以 headless 模式渲染并调用 `print_to_pdf`。渲染路径可设置纸张和背景，不内置浏览器。[浏览器渲染实现](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/html/browser.rs#L554)

文档还规定 HTML 及其资源只能访问公开 HTTP(S) 地址。现有业务页面若依赖内网、登录态或本机资源，需要验证接入方式，不能仅根据“支持 HTML”认定无需适配。[技术说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/docs/printbridge-technical.md)

**无人登录：Linux 有具体实现证据。**systemd 单元使用 `printbridge` 系统用户运行 `print-bridge serve`，挂到 `multi-user.target`；这支持其 Linux Headless 产品声明。本轮未确认 Windows Service 或 macOS LaunchDaemon 对等能力，也没有实测 Linux 开机打印。[systemd 单元](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/apps/server/packaging/systemd/print-bridge.service)

**状态：平台后端存在差异。**所读 Windows 后端因 SumatraPDF 提交没有暴露系统任务 ID，跟踪结果返回 `Unknown`；CUPS 后端有任务查询逻辑。不能只看统一 SDK 的状态枚举就认定各平台可观测性相同。[Windows 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/windows.rs#L120) · [CUPS 后端](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/crates/runtime/src/printing/cups.rs)

**资源成本：有路径证据，无比较基准。**当前 HTML `render` 路径启动浏览器、导航、等待并生成 PDF；这说明静默打印仍包含渲染开销，不能据 Rust/Tauri 或“不内置浏览器”推导低配机器上的性能排名。

已核对主项目 `LICENSE` 为 Apache-2.0；第三方说明单独列出随 Windows 产品分发的 SumatraPDF 及其 GPLv3 许可。不能把主仓库许可概括为整个安装包的统一许可。[LICENSE](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/LICENSE) · [第三方说明](https://github.com/vergil-lai/print-bridge/blob/e0f0b8c5365f38c300ac6accb7a322061dec2b69/THIRD_PARTY_NOTICES.md)

## print-bridge-jssdk

源码基线：`c78844da3568c6fac0a82f011a94191b0a97b958`。它是上述 Agent 的接入库，不是另一个独立打印运行时。

**接入：项目自述与代码一致。**SDK 提供连接、单任务、批量任务、打印机查询和状态事件；实现使用 WebSocket，支持浏览器内置实现或 Node.js 实现。[README](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md) · [客户端实现](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/src/index.ts)

`raw-html` 的内容被序列化后发送给 Agent；该接口不能自动等同于“抓取当前页面完整打印状态”。表单当前值、资源、字体和 canvas 的保真仍应使用业务样本验证。

**状态：文档明确区分。**`print()` 的 Promise 等待 `queued` 或错误；`submitted` 表示提交系统队列，`completed` 表示系统或驱动层观察到结束，均不承诺物理出纸；`unknown` 容纳不可跟踪或超时场景。[状态说明](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/README.md#状态监听)

源码构造 `ws://host:port/ws`；真实浏览器的本地网络授权、HTTPS 页面接入和企业策略仍需独立验证，SDK 可构造连接不代表所有浏览器部署默认放行。

已核对 SDK 的 `LICENSE` 为 MIT；这不替代 Agent 或其第三方组件的许可。[SDK LICENSE](https://github.com/vergil-lai/print-bridge-jssdk/blob/c78844da3568c6fac0a82f011a94191b0a97b958/LICENSE)

## HttpPrinter

**产品能力：官方自述。**官网列出网页与 Office/PDF 静默打印、拖拽报表设计、本地控件、打印浏览器和打印服务器等形态；这些形态应分开评价。[官网](https://www.httpprinter.com/)

**HTML：官方文档有具体接入例子。**HTML 批量示例向本地 `/printreport` 发请求，另有 HTML 双面参数说明及 WebSocket PDF 静默打印示例；不能将它归类为只支持专有报表模板。[HTML 批量示例](https://wiki.httpprinter.com/article/1780744713794) · [HTML 双面参数](https://wiki.httpprinter.com/article/1780744773957) · [PDF 静默打印](https://wiki.httpprinter.com/article/1780131178245)

该批量示例使用同步 Ajax；这属于示例调用方式的代码证据，不能扩展成“产品所有接口必然阻塞”。PrintKit 的页面响应性测试需要同时检查调用方与本地执行器。

**平台：有官方发布清单。**本轮读取下载页使用的公开目录接口，见到 Windows 和 Linux AMD64/ARM64 的 HttpPrinter 包，以及 Windows 打印浏览器包；官方知识库也有银河麒麟运行说明。[下载页](https://www.httpprinter.com/file-browser.html) · [公开目录数据](https://www.httpprinter.com/api/directory-structure) · [麒麟说明](https://wiki.httpprinter.com/article/1781666631399)

**待验证。**本轮没有充分材料确认 macOS 本机执行、无人登录生命周期、现代 CSS 保真范围或打印完成字段的设备级语义；这些未知项不等于不支持。官网的资源优化、状态监控描述尚无本轮可复现的性能或出纸验证数据。

官网列有商业购买与源码版本方案，但本轮未核对完整许可和再分发条款；不根据价格卡片推断二次开发授权范围。

## 可选补充：QZ Tray

官方介绍采用本地 WebSocket 连接，支持 HTML、PDF、图片及原始指令；静默调用涉及消息签名。[入门说明](https://qz.io/docs/getting-started)

官方 Headless 文档列出 Windows、Linux、macOS 命令，并要求预先配置证书白名单，避免无界面时无法处理授权对话框。这值得作为无人值守接入的对照，但不等于本轮已验证三平台开机未登录及所有文档类型。[Headless 文档](https://qz.io/docs/headless)

本轮不展开其性能、设备状态与许可比较，相关项目均保留待验证。

## 对 PrintKit 的约束

1. 不把“本地代理、静默、HTML 转 PDF、CLI、Linux 无桌面、分层状态”宣称为独有能力；参考项目已经提供不同程度的证据。
2. 现有 HTML/CSS 接入应以页面样本和迁移成本验证，包括动态表单、字体、图表、内网资源、分页与背景。
3. 平台矩阵必须分别记录桌面登录、锁屏、注销、开机未登录；“隐藏窗口”不能代替系统服务验证。
4. 任务接收、系统提交、系统报告结束、物理出纸应保持明确区分；未知结果要有恢复和防重复策略。
5. 低资源成本应以同机、同文档、同驱动测试证明，测量整条链路的进程树，而不只测核心服务。
6. 在确定自行实现、集成或借鉴代码之前，再核对目标版本及所有随附组件的许可。

本轮结论仅建立可追溯的比较起点。PrintKit 的差异与价值需要由后续原型、三平台生命周期验证和真实打印基准证明。
