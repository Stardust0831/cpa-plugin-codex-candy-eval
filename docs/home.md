# CPA Home 独立测试面板

Home 目前没有提供本插件需要的通用管理页面和模型执行回调。因此，Home 适配作为本仓库的可选启动方式，复用原有测试引擎和界面，通过 Home 已有 Management API 连接账号。它不是装进 Home 后就能显示菜单的动态库。

无需修改 Home 或 CPA、安装补丁、重启集群或更换管理面板。停止此进程即可撤下测试面板；默认监听 `127.0.0.1:8320`，Home 继续使用自己的端口。

## 启动

需要 Go 1.24 或更新版本。该适配按 CPA Home v1.1.0 的 v8 Management API 实现，目前只在模拟 Home 环境中验证。

```sh
mkdir -p dist
go build -trimpath -o dist/cpa-home-codex-eval ./cmd/cpa-home-codex-eval
cp home.env.example home.env
chmod 600 home.env
```

在 `home.env` 中填写 Home 地址、Home 管理密钥以及独立的面板访问密钥：

```dotenv
CPA_HOME_URL=http://127.0.0.1:8317
CPA_HOME_MANAGEMENT_KEY=你的Home管理密钥
CPA_HOME_EVAL_KEY=另设一个面板访问密钥
CPA_HOME_EVAL_LISTEN=127.0.0.1:8320
CPA_HOME_EVAL_DATA_DIR=./data
```

这两个密钥必须不同；Home 管理密钥只保留在服务端，浏览器登录填写 `CPA_HOME_EVAL_KEY`。启动命令：

```sh
set -a
. ./home.env
set +a
./dist/cpa-home-codex-eval
```

`home.env` 是 Shell 环境文件，含空格或 Shell 特殊字符的值请用单引号包围。程序不会自动读取该文件。也可以由服务管理器注入上述环境变量；密钥不使用命令行参数传递。

浏览器打开 `http://127.0.0.1:8320/`。远程服务器可通过 SSH 转发这个端口；若需直接访问，明确设置监听地址并配置 HTTPS。可通过 `-listen`、`-home-url`、`-data-dir` 覆盖非密钥配置。

## 范围和区别

- 支持 Home 中的 Codex OAuth 账号，以及糖果、指纹、ModelTrace、鹈鹕四种测试。尚不支持 Home 的 API Key 提供商账号。
- 测试按照 Home 数据库中的账号 ID 指定凭证，不依赖 CPA 工作节点的认证文件目录。停用或暂不可用的账号会跳过。
- 账号、模型目录为只读；启停、priority、集群配置和定时巡检自动调整仍在 Home 中操作。本面板不提供通用管理 API 转发，也不向浏览器返回凭证下载内容。
- Home 的 `requests/api-call` 在 Home 服务端使用所选账号的 Token，并沿用其代理设置。Home 必要时可能刷新账号 Token；测试会消耗账号额度。
- **每次上游请求最多 60 秒**，由 Home 的接口限制；响应由 Home 缓冲后返回，界面仍显示每项测试的进度。修改本面板超时不能延长该限制。
- 这条测试路径不经过 CPA 工作节点的协议转换、调度、亲和性、混淆和常规请求用量统计。结果用于评估所选账号的回答，不能代表完整集群链路的表现。适配只做基本 Codex Responses 请求规范化和全局 OAuth 模型别名解析，不复刻 CPA 执行器。
- 测试历史独立写入 `CPA_HOME_EVAL_DATA_DIR/cpa-home-codex-eval-state.json`，目录默认 `./data`；不写 Home 数据库或原有动态库的状态文件。保留每种测试最近 20 条记录。

原有 CPA 动态库入口、插件商店安装方式和发布版本保持兼容。Home 面板需单独编译和启动，不会自动占用任何端口。

## 模拟验证

```sh
go vet ./...
go test -race ./...
node scripts/test-ui.cjs
node scripts/test-home-ui.cjs
python3 scripts/test-home-deployment.py --binary ./dist/cpa-home-codex-eval
```

Go 集成测试和部署脚本使用本地模拟 Home 接口与合成凭证，验证账号读取、模型请求、四种测试及历史读取。部署脚本启动真实面板二进制，并在重启后检查历史；不连接真实 Home 或消耗真实账号额度。
