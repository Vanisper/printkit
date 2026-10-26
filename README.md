# printkit

printkit 是一个独立的本地打印系统，为 Web、桌面应用和自动化程序提供可复用的打印能力。产品目标是：**复用现有单据，在客户机上异步提交、静默打印，并查询任务进展和失败原因。**

当前实现是 Go 文档产物服务：

```text
提交 HTML / PDF → 持久化受理 → 生成或保存 PDF → 查询状态 → 下载产物
```

服务提供调用方授权、请求去重、后台执行和重启恢复。HTML 由独立 Chrome 生成 PDF，已有 PDF 原样保存。`output_ready` 表示 PDF 可下载；系统打印队列和真实设备接入属于后续阶段。

## 快速开始

安装 Go；所需工具链和依赖版本以 [go.mod](go.mod) / [go.sum](go.sum) 为准。HTML 渲染还需要本机 Chrome 和业务所需字体，它们独立于 Go 可执行文件安装。

在项目根目录执行：

```sh
make build
./bin/printkit init --data-dir work/dev
./bin/printkit token --data-dir work/dev
make run
```

打开 [文档验证页](http://127.0.0.1:9148/demo/)，粘贴 `token` 命令输出的令牌，提交内置样例或本地 HTML / PDF。页面支持任务轮询、按编号恢复查询及下载摘要校验，令牌只保存在当前页面内存中。

`init` 创建 `work/dev/config.json`；配置已存在时会报错并保留原文件。Chrome 无法自动找到时，在 `chrome_path` 中填写可执行文件路径；macOS 常见路径为 `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`。修改配置后重启服务。

服务默认监听 `127.0.0.1:9148`。每个调用方使用独立令牌和准确的 Origin 白名单；接入其他来源时，将其加入对应调用方的配置。配置和任务数据库保存在所选数据目录中，字段与接口说明见 [系统设计](docs/design.md)。

HTML 输入为 UTF-8 静态打印稿：CSS 内联，图片和字体通过 data URL 携带，并保留 `@media print` 与 `@page`。提交前需将表单当前值和 canvas 转换为静态内容；单据脚本、外部资源和本地文件读取会被拒绝。[双联单](testdata/dual-copy.html)与[汇总单](testdata/summary.html)是合成验证样例，业务接入需核对自己的内容、字体和分页。

## 开发与检查

| 命令 | 用途 |
| --- | --- |
| `make check` | 检查 gofmt，运行 go vet 和默认测试 |
| `make race` | 运行竞态检查 |
| `make integration` | 使用实际 Chrome 验证渲染和 HTTP 链路 |
| `make acceptance` | 验证单据页数与 A4、跨域验证页和 CLI 中断恢复 |
| `make build` | 无 cgo 构建，产物为 `bin/printkit` |
| `make run` | 使用 `work/dev` 配置启动开发服务 |

浏览器检查可通过 `CHROME=/路径` 指定可执行文件。`make integration` 对应 `PRINTKIT_CHROME_TEST=1`；`make acceptance` 对应 `PRINTKIT_ACCEPTANCE_TEST=1`，后者的 CLI 中断恢复检查在 macOS / Linux 运行。检查范围和环境限制见 [验证范围](docs/design.md#验证范围)。

Windows PowerShell 可直接使用 Go 命令：

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o bin/printkit.exe ./cmd/printkit
./bin/printkit.exe init --data-dir work/dev
./bin/printkit.exe token --data-dir work/dev
go run ./cmd/printkit serve --data-dir work/dev
```

默认检查为 `gofmt -l cmd internal`、`go vet ./...` 和 `go test ./...`。PowerShell 设置 `$env:PRINTKIT_CHROME_TEST = "1"` 后执行 `go test -count=1 ./...` 可运行浏览器集成检查；浏览器路径使用 `PRINTKIT_CHROME_PATH`。竞态检查为 `go test -race ./...`，需要启用 cgo 并安装可用的 C 工具链。

提交前运行格式、静态分析和相关测试。新增行为优先验证调用契约、持久化与恢复、授权及真实浏览器行为；升级依赖时同步更新锁定文件并复核相关链路。中文注释说明调用契约和非直观约束，文档维护当前行为、职责与使用方式，调研依据集中在 [参考资料](docs/references.md)。

## 项目结构

```text
cmd/printkit/             命令行、配置、模块组装与进程退出
internal/httpapi/         HTTP 校验、授权、接口和嵌入验证页
internal/jobs/            任务规则、SQLite 事务、串行执行与恢复
internal/render/          排版设置、静态 HTML 校验与 Chrome 渲染
testdata/                合成单据样例
.github/workflows/        自动化检查与构建配置
```

项目保持一个 Go module。`cmd` 组装各模块并管理数据目录的独占执行权；`httpapi` 通过任务接口提交、查询和下载；`jobs` 持有任务与产物事务，并将任务保存的排版设置交给渲染器；`render` 封装资源检查、浏览器进程和 PDF 生成。

| 文档 | 内容 |
| --- | --- |
| [README.md](README.md) | 项目目标、运行入口和开发检查 |
| [docs/design.md](docs/design.md) | 当前架构、输入与任务契约、HTTP 接口及验证范围 |
| [docs/references.md](docs/references.md) | 技术依据、打印接口和相关项目调研 |

## 接入真实打印

下一阶段选择一套实际使用的操作系统、浏览器、打印机和驱动，使用业务双联单与汇总单验证纸张、分页、静默提交和队列状态。受理、系统队列反馈与实际出纸分别验收；提交结果不明时由人工核对后补打。

Windows、macOS、Linux 及无人登录运行按实际部署需求扩大验证范围。桌面管理、CLI / SDK、远程派发和模板设计按对应场景推进。printkit 独立运行，也可以与 [weblauncher](https://github.com/Vanisper/weblauncher) 组合安装。
