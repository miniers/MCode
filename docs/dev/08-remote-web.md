# 08 远程协议与 Web 端

协议说明见 [`docs/remote-control-protocol.md`](../remote-control-protocol.md)，机器契约见 [`docs/remote-control.schema.json`](../remote-control.schema.json)。本章讲实现结构。

## 服务端结构

| 文件（`internal/remote/`） | 内容 |
|---|---|
| `server.go` | WebSocket 和静态资源；`staticRouteMap` 白名单；`/assets/` 正则和长缓存；Host、Origin 校验；`/admin/remote/*` 仅本机 |
| `dispatcher.go` | JSON-RPC 解析、鉴权、参数校验、方法路由 |
| `capabilities.go` | 内置方法清单和 `defaultFeatures` |
| `extensions.go` | `RegisterMethod`、`decodeExtParams`（严格解码）、`ExtError`、`SetService`、`backendService` |
| `auth.go`、`pairing.go`、`device_store.go` | 鉴权、配对、设备存储 |
| `lan.go` | 局域网地址，物理网卡优先 |
| `tls.go`、`acme.go` | 自签名和 ACME 证书 |
| `tailnet/` | 内嵌 tsnet 的 Tailscale 节点：启停、状态、WhoIs |
| `webpush.go` | RFC 8291 加 VAPID |

`internal/remote/wire/` 是对外的数据类型。`internal/app/remote_service.go` 负责启停服务。

连接参数：ping 30 秒，pong 10 秒，错过 2 次断开。未认证连接上限 4 个。默认监听 `0.0.0.0:43117`，TLS 建议 8443。

## Tailscale 入口

除局域网和自定义域名 HTTPS 之外还有第三种入口：`internal/remote/tailnet` 用 tsnet 把 mcode 内嵌成 tailnet 里的一台独立设备，不需要 NAT、端口映射或公网 IP。

状态目录是数据目录下的 `tailnet/`，权限 `0700`，存放节点密钥和 `tailscaled.state`，不能进仓库，也不能放在同步盘。节点默认主机名 `mcode`，用 `remote.tailscale_hostname` 改；总开关是 `remote.tailscale_enabled`，缺省关闭。

节点上线后在 tailnet 的 443 上 `ListenTLS` 提供服务，证书由 Tailscale 自动签发和续期，所以 tailnet 必须开启 MagicDNS 和 HTTPS Certificates；入口走 tailnet 内地址，不使用 funnel。

入口常量是 `remote.EntryTailscale`，`Entry.Secure()` 对 `tls` 和 `tailscale` 都返回 true，Host、Origin 校验和机密类答案的放行都只看 `Secure()`，不需要为 Tailscale 单独开口子。listener 到这一步已经完成握手，因此用 `Server.ServeTLSListener` 启动；`WhoIs` 解出的对端身份只用于展示和审计，不参与鉴权，鉴权仍走配对码和 token。

启动逻辑在 `internal/app/remote_tailscale.go`：局域网和 TLS 起来之后用后台协程拉起 Tailscale，失败只写状态，不影响另外两个入口。状态分四种：未启用、待登录（带可直接打开的 AuthURL）、缺 HTTPS 证书、运行中（带 FQDN）。配对主链接按 Tailscale、TLS、局域网的顺序取，节点真正开始服务后才写入地址，链接随之切换。

设置页有「Tailscale」卡片和高级区的「Tailscale 节点」，配对卡可以切换二维码走哪个入口。Tailscale 地址是一个新的 origin：浏览器要在该地址上重新配对，推送订阅也要重新订阅。`cmd/mcode-server` 不接 Tailscale，体积不变；`cmd/mcode` 二进制约大 16 MB。

## 鉴权与配对

未认证连接只能调用 `session.pair` 和 `session.authenticate`。

- 配对码有两种：6 位数字或 64 位 hex。二者同时有效、同时过期、一次性。
- 每个来源 10 分钟内失败 5 次，封禁 5 分钟。
- 设备存 `remote-devices.json`，只保存 token 的哈希，闲置 30 天过期。

## 方法

### 内置方法

在 `capabilities.go` 列出，由 `dispatcher.go` 路由：`session.*`、`capabilities.get`、`kanban.*`、`goals.*`、`projects.list`、`providers.*`、`threads.*`、`requests.*`、`swarm.tiers.*`、`files.*`、`git.status`、`git.branches`、`scripts.*`、`composer.quota`、`attachments.upload`、`push.*`。

### 扩展方法

通过 `RegisterMethod` 在 `init()` 注册：

| 文件 | 方法 |
|---|---|
| `attention_ext_methods.go` | `attention.list`、`resolve`、`confirmSuggestion` |
| `automation_ext_methods.go` | `automations.*` |
| `git_ext_methods.go` | `git.changedFiles`、`commitSelected`、`pr.detail`、`pr.list`、`stash.*` |
| `memory_ext_methods.go` | `memory.proposals.list`、`accept`、`reject` |
| `message_search_methods.go` | `messages.search` |
| `persona_methods.go` | `providers.personas`、`threads.setPersona` |
| `always_allow_methods.go` | `requests.alwaysAllowed.list`、`revoke` |
| `settings_ext_methods.go` | `settings.get`、`set` |
| `swarm_ext_methods.go` | `threads.swarm.dismissInterrupted`、`interrupted`、`report`、`resume`、`retryBee` |
| `steward_topics.go` | `steward.open`、`steward.topics.*` |
| `mcp_ext_methods.go` | `mcp.servers.list` |
| `project_ext_methods.go` | `projects.add`、`projects.validatePath` |

服务端通知：`attention.changed`、`automations.changed`、`memory.proposals.changed`、`projects.changed`、`settings.changed`、`steward.changed`。

### 能力协商

客户端用 `capabilities.get` 取方法和 features。features 见 `capabilities.go`，例如 `threadHistoryPaging`、`threads.queue`、`threads.swarm`、`push.offline`。新增不兼容能力时要加 feature，让 Web 端按能力显示。

## 新增 RPC 的流程

1. 在 `internal/remote/xxx_methods.go` 的 `init()` 调 `RegisterMethod`，用 `decodeExtParams` 解参。
2. 定义 `stewardExtServiceOf` 这类取服务函数，未注入时回 `MethodNotFound`。
3. 在 `internal/app/xxx_remote.go` 的 `init()` 调 `registerRemoteWiring`，里面用 `SetService` 注入实现。
4. 更新 `docs/remote-control.schema.json`，类型放到 `internal/remote/wire/`。
5. 在 `web/` 运行 `npm run generate:contract`，生成 `web/src/types/generated.ts`。
6. 在 `web/src/context/remote*Api.ts` 按域增加调用。

app 层已有的接线文件：`internal/app/remote_settings.go`、`remote_mcp.go`、`remote_persona.go`、`remote_attention.go`、`remote_swarm_ext.go`、`remote_steward.go`、`remote_memory.go`、`remote_automation.go`、`remote_git.go`。

## 事件流

`threads.subscribe` 之后，服务端经 `StreamHub` 推送事件。`runID` 和 `sequence` 用来发现丢帧，单帧超过 512KB 时改发 resync，客户端重新拉取历史。

Web 端的处理在 `web/src/context/remoteThreadSubscription.ts`：缓冲、按序应用、重订阅。

## 推送

- `webpush.go` 实现加密和 VAPID。私钥在 `remote-webpush-vapid.pem`，订阅和设备一起存。
- `VAPIDSubjectFor`：有 TLS 域名用 `https://域名`，否则用本机入口地址。
- Service Worker 只在安全上下文注册（`web/src/main.tsx`），所以局域网 HTTP 访问时没有推送。

## Web 前端

### 结构

| 位置 | 内容 |
|---|---|
| `web/src/main.tsx` | 入口：视口变量、注册 Service Worker |
| `web/src/App.tsx` | hash 路由和懒加载 |
| `web/src/context/RemoteContext.tsx`、`web/src/context/remote*Api.ts` | 连接和全局状态，按域拆分的 API |
| `web/src/context/remoteConnection.ts` | 连接管理 |
| `web/src/services/rpc.ts` | `McodeRpcClient` |
| `web/src/services/storage.ts` | 配对码解析（支持 `#pair=`）和本地存储 |
| `web/src/services/draftStorage.ts` | 草稿 |
| `web/src/types/generated.ts`、`contract.ts` | 生成的类型和手写补充 |

路由：`home`、`board`、`settings/<page>`、`new/<projectID>`、`thread/<id>`（子视图 `agents`、`review`、`git`、`files`、`scripts`）、`task`、`goal`。

主要组件都在 `web/src/components/`：`ThreadView`、`ThreadTimeline`、`ThreadComposer`、`ThreadList`、`ThreadDraftView`、`KanbanBoard`、`TaskDetailModal`、`Swarm*`、`Steward*`、`Automation*`、`Git*`、`FileBrowserSheet`、`ScriptsSheet`、`SettingsForm`、`AttentionSheet`、`MemoryProposalsSheet`、`Header`。

### 构建

- `npm run build` 依次运行 `generate:contract`、`tsc`、`vite build`。
- 输出到 `internal/webassets/dist`，由 `internal/webassets` 用 `//go:embed all:dist` 内嵌。
- 固定入口 `remote-app.js`、`remote-app.css`、`remote-index.html`（构建后把 `index.html` 改名）。`buildID` 取内容哈希写入 `<meta name="mcode-build-id">`。
- 分块输出到 `assets/[name]-[hash].js`。`manualChunks` 划分 vendor-react、vendor-query、vendor-highlight、vendor-markdown。公式相关的包不得进 vendor-markdown，否则懒加载失效。

### 拆包时要同步四处

1. `web/vite.config.ts` 的 chunk 输出。
2. `internal/remote/server.go` 的 `staticRouteMap` 和 `assetChunkPathPattern`。
3. `web/public/remote-boot.js` 的路径校验。
4. `web/public/remote-sw.js` 的缓存。

少改一处，线上就可能拿不到资源或拿到旧版本。

### 检查脚本

`web/scripts/` 下：`check-device-session.mjs`、`check-pairing.mjs`、`check-thread-scroll.mjs`、`generate-contract.mjs`。
