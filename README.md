# printkit

printkit 是一个独立的本地打印系统，为 Web、桌面应用和自动化程序提供可复用的打印能力。

产品目标是：**复用现有单据，在真实客户机上异步提交、静默打印，并查询任务进展和失败原因。**

当前已有 Go 最小原型，在没有打印机的条件下验证软件链路：

```text
提交 HTML / PDF → SQLite 持久化 → 生成或保存 PDF → 查询状态 → 下载产物
```

服务支持调用方授权、重复请求去重、网页关闭后的执行和重启恢复。HTML 通过独立 Chrome 生成 PDF，已有 PDF 原样保存。`output_ready` 表示 PDF 可以下载，尚未接入系统打印队列和真实设备。

## 快速开始

需要 Go 1.26 和本机安装的 Chrome。项目通过 `go.mod` 指定 `go1.26.5` 工具链；Chrome 与中文字体是独立运行依赖，不包含在 Go 可执行文件中。

在项目根目录执行：

```sh
make build
./bin/printkit init --data-dir work/dev
./bin/printkit token --data-dir work/dev
make run
```

打开 [文档验证页](http://127.0.0.1:9148/demo/)，粘贴 `token` 命令输出的令牌，提交内置 HTML 样例或选择本地 HTML / PDF 文件。验证页支持任务轮询、按编号恢复查询和下载摘要校验，令牌仅保留在当前页面内存中。

`init` 创建 `work/dev/config.json`，已有配置不会被覆盖。Chrome 无法自动找到时，在该文件的 `chrome_path` 中填写可执行文件路径；macOS 常见路径为 `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`。修改配置后重启服务。

服务默认监听 `127.0.0.1:9148`，配置中的每个调用方有独立令牌和准确的 Origin 白名单。接入其他页面来源时，需要增加对应 Origin；配置和任务数据库均保存在所选数据目录中。

HTML 输入使用 UTF-8 静态打印稿，内联 CSS，通过 data URL 携带图片和字体，保留 `@media print` 和 `@page`。原型拒绝脚本、外部资源和本地文件读取。[双联单](testdata/dual-copy.html)与[汇总单](testdata/summary.html)是合成验证样例，真实业务单据仍需另行核对。

## 开发与检查

| 命令 | 用途 |
| --- | --- |
| `make check` | 检查 gofmt、运行 go vet 和默认测试 |
| `make race` | 运行竞态检查 |
| `make integration` | 启用实际 Chrome 的集成测试，可通过 `CHROME=/路径` 指定浏览器 |
| `make build` | 无 cgo 构建，产物为 `bin/printkit` |
| `make run` | 使用 `work/dev` 配置启动开发服务 |

Windows PowerShell 无需 Make，可使用等价的 Go 命令：

```powershell
$env:CGO_ENABLED = "0"
go build -trimpath -o bin/printkit.exe ./cmd/printkit
./bin/printkit.exe init --data-dir work/dev
./bin/printkit.exe token --data-dir work/dev
go run ./cmd/printkit serve --data-dir work/dev
```

检查命令为 `gofmt -l cmd internal`、`go vet ./...` 和 `go test ./...`。实际 Chrome 集成检查使用 `PRINTKIT_CHROME_TEST=1`，可选 `PRINTKIT_CHROME_PATH` 指定浏览器路径；PowerShell 通过 `$env:PRINTKIT_CHROME_TEST = "1"` 设置环境变量后执行 `go test -count=1 ./...`。竞态检查使用 `go test -race ./...`，需要启用 cgo 及可用的 C 工具链。

开发时保持 gofmt 格式，通过 vet 和测试后再提交。新增行为优先验证调用契约、异常恢复及真实浏览器边界；依赖版本固定在 `go.mod` / `go.sum`，升级时一并验证。macOS 已通过运行与 Chrome 集成测试，Windows amd64 / Linux amd64 已通过无 cgo 交叉构建，运行行为尚待验证。CI 已配置，尚未在远端运行。

## 项目结构

```text
cmd/printkit/             命令行、配置、模块组装与退出处理
internal/httpapi/         HTTP 校验、授权、接口和嵌入验证页
internal/jobs/            任务规则、SQLite 事务、串行执行与恢复
internal/render/          Chrome 生命周期、静态 HTML 与资源检查
testdata/                合成单据样例
.github/workflows/        自动化检查与构建配置
```

`cmd` 负责组装三个内部模块。`httpapi` 只负责接入边界，通过任务接口提交、查询和下载；`jobs` 持有任务与产物事务，并通过渲染接口执行 HTML；`render` 封装浏览器进程及 PDF 生成。

| 文件 | 内容 |
| --- | --- |
| [README.md](README.md) | 项目目标、运行入口和开发检查 |
| [docs/design.md](docs/design.md) | 原型技术栈、实现方案和验收顺序 |
| [docs/references.md](docs/references.md) | 参考项目、技术选型依据及来源 |

## 接入真实打印

有设备后，先选定一套操作系统、浏览器、打印机和驱动，复用现有业务的双联单与汇总单验证纸张、分页、静默提交和队列状态。受理、系统队列反馈与实际出纸需要分别验收，提交结果不明时由人工核对后补打。

Windows、macOS、Linux 及无人登录运行仍是后续目标，按实际部署需求扩大验证范围。桌面管理、CLI / SDK、远程派发和模板设计按对应场景推进。printkit 独立运行，也可以与 [weblauncher](https://github.com/Vanisper/weblauncher) 组合安装。
