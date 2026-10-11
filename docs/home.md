# CPA Home 测试插件

Home 版插件 ID 为 **`cpa-home-codex-eval`**。通过 Home 插件商店安装后，由 Home 的动态库加载器启动测试面板，复用原有测试引擎和界面，通过 Home 已有 Management API 连接账号。

Home 尚不暴露插件资源页面，所以面板仍使用独立端口，默认地址为 **`http://127.0.0.1:8320/`**，不出现在 Home 的插件菜单里。无需修改 Home 或 CPA 源码。

## 从 Home 插件商店安装

该适配按 CPA Home v1.1.0 的 v8 Management API 实现，目前在模拟 Home 环境中验证。商店安装使用预编译动态库，无需服务器安装 Go。

1. 在 Home 的 **系统配置 → 插件设置 → 第三方插件源** 中添加以下 URL（每行一个 URL），保存后返回插件页刷新。它是自定义源，尚未收录到内置官方源。

```text
https://raw.githubusercontent.com/Stardust0831/cpa-plugin-codex-candy-eval/feat/cpa-home-standalone/registry.json
```

这是公开源，无需创建“添加认证规则”，也无需填写其中的凭据或 Registry / Metadata / Artifact 选项。认证规则只控制下载请求携带的认证信息，不会新增商店目录。第三方作者的插件也可以被内置官方目录收录；安装时的“第三方插件”提示与是否添加了自定义源是两回事。

2. 为**运行面板的那个 Home 进程**配置以下环境变量，再重启该 Home 进程使环境生效。可使用 Compose 的 `environment` / `env_file` 或 systemd 的 `EnvironmentFile`。这些值不放进会同步到 CPA 工作节点的插件配置中。

```dotenv
CPA_HOME_URL=http://127.0.0.1:8327
CPA_HOME_MANAGEMENT_KEY=你的Home管理密钥
CPA_HOME_EVAL_KEY=另设一个面板访问密钥
CPA_HOME_EVAL_LISTEN=127.0.0.1:8320
CPA_HOME_EVAL_DATA_DIR=/持久化目录/cpa-home-codex-eval
```

两个密钥必须不同。`CPA_HOME_URL` 必须填写从 Home 进程所在环境可访问的实际 Home 地址，上例使用 Home 默认端口 `8327`；它与 CPA 节点常用的 `8317` 不同。管理密钥只在服务端使用，浏览器登录填写 `CPA_HOME_EVAL_KEY`。HA 部署只给希望运行面板的 Home 实例注入这组变量；工作节点不要配置这组变量。

3. 在 Home 插件商店中搜索 **`cpa-home-codex-eval`** 或 **`CPA Home · Codex 降智测试`**，点击安装。启用全局插件开关，并在该插件配置中启用 **`load-in-home: true`**。相关 Home 配置结构如下，请合并到现有配置，保留其他插件和安装时生成的 `store` 字段：

```yaml
plugins:
  enabled: true
  store-sources:
    - https://raw.githubusercontent.com/Stardust0831/cpa-plugin-codex-candy-eval/feat/cpa-home-standalone/registry.json
  configs:
    cpa-home-codex-eval:
      enabled: true
      load-in-home: true
      # store 字段由插件商店安装自动写入，保留即可。
```

Home 的商店默认只记录安装 manifest 并向节点分发；`load-in-home` 是 Home 自己要求的控制面加载开关。只点安装、没有配置环境变量和这个开关时，不会启动面板。Home 使用数据库配置，应通过 Home 管理界面/API 修改生效配置。

4. 打开面板 `http://127.0.0.1:8320/`，使用独立的面板访问密钥登录。远程机器可使用 SSH 转发：

```sh
ssh -L 8320:127.0.0.1:8320 用户名@Home服务器
```

在自己电脑打开同一地址即可。默认绑定回环地址；需要容器端口映射或反向代理时，可把 `CPA_HOME_EVAL_LISTEN` 改为 `0.0.0.0:8320` 并配置相应端口映射及 HTTPS。端口监听在加载动态库的 Home 进程所在网络空间；容器内的 `127.0.0.1` 是容器自身。

停用或卸载插件会关闭面板端口并结束测试请求，历史文件保留；再次启用会继续使用同一历史目录。更新通过商店执行。Home 可能仍将该动态库分发到 CPA 节点；未配置本机密钥的节点仅注册插件元数据，不启动面板、不访问账号、不读写测试历史。

插件页的“节点回报：需要”表示当前启用的商店插件需要 CPA 节点回报安装和加载结果，本身不是失败或尚未回报的状态。具体结果应查看各节点的回报详情；成功回报后仍可能显示“需要”。这项集群状态不代表 Home 面板已启动，面板需由选定 Home 实例完成本机配置及加载后访问。

## 可选：独立程序启动

保留之前的独立运行方式，不要与商店版本同时绑定同一端口或使用同一历史目录。需要 Go 1.24 或更新版本：

```sh
git clone --branch feat/cpa-home-standalone https://github.com/Stardust0831/cpa-plugin-codex-candy-eval.git
cd cpa-plugin-codex-candy-eval
mkdir -p dist
go build -trimpath -o dist/cpa-home-codex-eval ./cmd/cpa-home-codex-eval
cp home.env.example home.env
chmod 600 home.env
```

填写 `home.env` 后启动：

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

原版 CPA 插件 ID 仍是 `cpa-codex-candy-eval`。Home 商店包使用独立 ID，避免安装时混淆或覆盖原版。两种商店包均遵循 CPA 的动态库 ABI、ZIP 命名及 SHA-256 校验规范。

## 模拟验证

```sh
go vet ./...
go test -race ./...
node scripts/test-ui.cjs
node scripts/test-home-ui.cjs
python3 scripts/test-home-deployment.py --binary ./dist/cpa-home-codex-eval
go build -tags 'cshared homepanel' -buildmode=c-shared -o dist/cpa-home-codex-eval.so ./cmd/cpa-codex-candy-eval
python3 scripts/test-home-deployment.py --library ./dist/cpa-home-codex-eval.so
python3 scripts/test-home-plugin.py ./dist/cpa-home-codex-eval.so
```

Go 集成测试和部署脚本使用本地模拟 Home 接口与合成凭证，验证账号读取、模型请求、四种测试及历史读取。部署脚本可启动真实面板二进制或通过 C ABI 加载商店动态库，并在重启后检查历史。生命周期测试验证无配置节点保持空闲、停用释放端口以及同一进程重新启用；不连接真实 Home 或消耗真实账号额度。
