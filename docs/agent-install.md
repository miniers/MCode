# MCode 安装指南（供 AI agent 执行）

## 执行规则

- 本文档的读者是 AI agent。agent 必须按顺序执行每一步。agent 必须运行每步的检查命令。
- 检查失败时，agent 必须停止。agent 必须把原始输出告诉用户。
- agent 不得替用户输入密码、API Key 或账号登录。需要这些信息时，停下来请用户提供或操作。API Key 必须由用户输入。
- 修改 MCode 的数据目录前，必须先退出 MCode。MCode 运行时不重新读取外部修改的 `config.json`。
- 已有的配置文件必须合并，不得整体覆盖。修改前必须备份。
- 完成后，按最后一节的模板向用户汇报。

## 第 1 步：检查环境

1. 检查 macOS 版本。

   ```sh
   sw_vers -productVersion
   ```

   版本必须为 14.0 或更高。

2. 检查处理器架构。

   ```sh
   uname -m
   ```

   结果必须为 `arm64`。任一条件不满足时，停止。告诉用户：发布页只有 Apple 芯片的 macOS 版本。

3. 设置数据目录。`MCODE_DATA` 有值时，使用该值。否则，使用 `~/.mcode`。后文用 `DATA` 指代数据目录。

   ```sh
   DATA="${MCODE_DATA:-$HOME/.mcode}"
   ```

## 第 2 步：安装 MCode

1. 退出正在运行的 MCode。MCode 未运行时，忽略此命令的错误。

   ```sh
   osascript -e 'quit app "MCode"'
   ```

2. 取得最新 DMG 地址并下载。

   ```sh
   url=$(curl -fsSL https://api.github.com/repos/miniers/MCode/releases/latest | grep -o 'https://[^"]*-arm64\.dmg' | head -1)
   curl -fL -o /tmp/mcode.dmg "$url"
   ```

3. 挂载 DMG。

   ```sh
   hdiutil attach -nobrowse -quiet -mountpoint /tmp/mcode-dmg /tmp/mcode.dmg
   ```

4. 检查挂载目录。确认其中有 `MCode.app`。

   ```sh
   ls /tmp/mcode-dmg
   ```

5. 复制 MCode。

   ```sh
   cp -R /tmp/mcode-dmg/MCode.app /Applications/
   ```

6. 卸载 DMG。

   ```sh
   hdiutil detach /tmp/mcode-dmg
   ```

7. 检查 Gatekeeper。

   ```sh
   spctl -a -vv /Applications/MCode.app
   ```

   Gatekeeper 拒绝应用时，必须先征得用户同意。用户同意后，运行以下命令。仓库的个人打包脚本 `scripts/package_personal.sh` 也执行同样的命令。

   ```sh
   xattr -dr com.apple.quarantine /Applications/MCode.app
   ```

   再次运行 `spctl -a -vv /Applications/MCode.app` 检查。

**警告：**使用 Magpie 网关时，第 5 步完成前，不得打开 MCode。首次打开会创建引导状态，后续写入的配置不会跳过首次启动引导。

## 第 3 步：安装智能体

1. 询问用户要安装哪些智能体。可选的智能体为 Claude、Codex、Antigravity（`agy`）和 pi。用户必须至少选择一个。推荐同时安装 pi。pi 是 MCode 推荐的子代理引擎。

2. 用户选择 Claude、Codex 或 pi 时，检查 Node.js 和 npm。

   ```sh
   command -v node npm
   ```

   缺少 Node.js 或 npm 时，请用户从 https://nodejs.org/ 安装。有 Homebrew 时，可以运行以下命令。

   ```sh
   brew install node
   ```

   安装后，再次检查 `node` 和 `npm`。

3. 安装用户选择的智能体。

   | 智能体 | 命令 |
   | :--- | :--- |
   | Claude | `npm install -g @anthropic-ai/claude-code` |
   | Codex | `npm install -g @openai/codex` |
   | Antigravity | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
   | pi | `npm install -g @earendil-works/pi-coding-agent` |

4. 检查已安装的智能体。只在命令中保留已安装的名称。

   ```sh
   command -v claude codex agy pi
   ```

5. 检查每个可执行文件所在的目录。MCode 启动时只在原有 `PATH` 之外补充以下目录。来源：`cmd/mcode/main.go`。

   - `/opt/homebrew/bin`
   - `/opt/homebrew/sbin`
   - `/usr/local/bin`
   - `~/.local/bin`
   - `~/.bun/bin`
   - `~/.cargo/bin`
   - `~/.npm-global/bin`
   - `~/.volta/bin`
   - `~/.version-fox/sdks/nodejs/bin`
   - `~/.version-fox/shims`
   - `~/.local/share/mise/shims`
   - `~/.asdf/shims`
   - `/usr/bin`
   - `/bin`
   - `/usr/sbin`
   - `/sbin`

   可执行文件不在这些目录时，记下绝对路径。在汇报中请用户到 MCode 设置的「模型与供应商」填写「可执行文件路径」。

6. 请用户完成登录。Claude 用户在终端运行 `claude`，然后输入 `/login`。Antigravity 用户在终端运行 `agy`，然后选择 Google 登录。其他智能体的用户在终端运行对应命令后按提示登录。

## 第 4 步：安装 Magpie 网关

用户不使用 Magpie 网关时，跳过第 4 步和第 5 步，直接执行第 6 步。首次打开 MCode 时显示引导。请用户在引导中点「只用直连」。

1. 安装 Magpie。项目地址：https://github.com/yetone/magpie。

   ```sh
   curl -fsSL https://usemagpie.ai/install.sh | sh
   ```

2. 请用户选择供应商并提供 API Key。请用户在终端输入 API Key 并运行以下命令。Magpie README 的示例是 `magpie provider add deepseek sk-…`。用户也可以在 Magpie 应用中打开 Providers → Add provider 自己添加。

   ```sh
   magpie provider add <预置名> <API Key>
   ```

3. 检查网关。

   ```sh
   curl -fsS http://127.0.0.1:3425/v1/models -H 'Authorization: Bearer magpie'
   ```

   返回 JSON 的 `data` 数组中，每项的 `id` 是模型 ID。请求失败时，请用户打开 Magpie 应用后重试。返回空列表时，回到第 2 小步。

4. 记下网关密钥。Magpie README 的本机网关示例使用 `magpie` 作为密钥。用户给出其他密钥时，使用用户的密钥。

5. 请用户为每个已安装的 Claude、Codex 和 pi 选择模型 ID。用户为 pi 至少选择一个模型 ID。

## 第 5 步：写入 MCode 配置

写入前，退出 MCode。

```sh
osascript -e 'quit app "MCode"'
```

MCode 未运行时，忽略此命令的错误。

1. 创建数据目录。

   ```sh
   mkdir -p "$DATA"
   ```

2. `config.json` 已存在时，备份文件。

   ```sh
   cp "$DATA/config.json" "$DATA/config.json.agent-bak"
   ```

   读出 `version` 字段。`version` 大于 9 时，停止。告诉用户：本文档不支持该版本。

3. 合并以下结构到 `"$DATA/config.json"`。文件不存在时，新建为该结构。缺少的字段由 MCode 使用默认值。只列出用户实际安装的智能体。

   `providers.custom` 已有 `magpie` 为 `true` 的条目时，复用该条目的 `id`。不得新增条目。所有 `endpointID` 必须与复用的 `id` 一致。

   ```json
   {
     "version": 9,
     "providers": {
       "magpieRoutingVersion": 1,
       "custom": [
         {"id": "magpie", "name": "Magpie", "baseURL": "http://127.0.0.1:3425/v1", "model": "", "enabled": true, "magpie": true}
       ],
       "magpieAgents": {
         "claude": {"mode": "inject", "selections": [{"endpointID": "magpie", "models": ["<模型 ID>"]}]},
         "codex": {"mode": "inject", "selections": [{"endpointID": "magpie", "models": ["<模型 ID>"]}]},
         "pi": {"mode": "inject", "selections": [{"endpointID": "magpie", "models": ["<模型 ID>"]}]}
       }
     }
   }
   ```

   | 字段 | 取值 | 含义 |
   | :--- | :--- | :--- |
   | `version` | `9` | 配置格式版本 |
   | `providers.magpieRoutingVersion` | `1` | 必须写入。否则，MCode 的一次性迁移会把 Codex 的 `inject` 改成 `off`。 |
   | `providers.custom[].magpie` | `true` | 表示这是 Magpie 网关。 |
   | `magpieAgents.<智能体>.mode` | `off`、`inject`、`global` | 对应界面「直连」「仅 mcode」「全局」。 |

   **警告：**不得写入 `global`。该模式需要 MCode 改写智能体的全局配置文件，只写 `config.json` 不会执行这一步。用户需要「全局」时，在汇报中请用户到 MCode 设置的「Magpie」页切换。

   有 `jq` 时，使用 `jq` 合并。没有 `jq` 时，使用 `python3 -I` 读写 JSON。合并后，运行以下任一命令检查格式。

   ```sh
   jq . "$DATA/config.json"
   ```

   ```sh
   python3 -I -m json.tool "$DATA/config.json"
   ```

4. 将网关密钥写入钥匙串。账户名中的 `magpie` 必须与上一步的 `id` 一致。请用户输入网关密钥并运行命令。

   ```sh
   security add-generic-password -U -s mcode -a providers.custom.magpie.apiKey -w '<网关密钥>'
   ```

   检查钥匙串。账户名中的 `magpie` 必须与上一步的 `id` 一致。

   ```sh
   security find-generic-password -s mcode -a providers.custom.magpie.apiKey -w
   ```

   MCode 从钥匙串读取网关密钥。`config.json` 中不存密钥。

5. 完成首次启动引导的设置。`"$DATA/ui-state.json"` 不存在时，不需要操作。`config.json` 已存在时，MCode 不显示引导。`ui-state.json` 已存在时，先备份文件，再把 `onboarding` 字段设为 `"completed"`。保留其他字段。修改后检查 JSON 格式。

   ```sh
   cp "$DATA/ui-state.json" "$DATA/ui-state.json.agent-bak"
   ```

   ```sh
   python3 -I -m json.tool "$DATA/ui-state.json"
   ```

## 第 6 步：启动与确认

1. 启动 MCode。

   ```sh
   open -a MCode
   ```

2. 使用 Magpie 网关时，请用户打开设置（⌘,）的「Magpie」页。请用户确认状态显示「已连接 · N 个模型」。

3. 显示「连接失败：」或「未设置 API Key」时，检查第 4 步第 3 小步和第 5 步第 4 小步。

## 汇报模板

- 已安装的 MCode 版本与路径：
- 已安装的智能体与路径：
- Magpie 网关的状态与模型数：
- 写入的配置项：
- 需要用户完成的事项：
  - 登录哪些智能体：
  - 是否需要解除 Gatekeeper 隔离：
  - 是否需要填写可执行文件路径：
  - 是否需要切换「全局」：
