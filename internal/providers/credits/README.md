# internal/providers/credits

各 provider 的预付费余额与 Zai、Cline、Droid/Factory 的方案额度读取，
纯 Go、可测试的服务。

## 入口

- `ExposesCredits(provider)`：只有 deepseek、cline、commandcode 为 true。
- `Read(ctx, provider, opts)`：读预付费余额；支持 deepseek、cline、commandcode，其余返回 `ErrUnsupported`。
- `ReadPlanWindows(ctx, provider, opts)`：读方案窗口；支持 zai、cline、droid（别名 factory），其余返回 `ErrUnsupported`。
- `FactoryClient`：解析 Factory（Droid）令牌（`Token`），并提供 Droid 的方案额度读取（`PlanWindows`）。

## 端点与认证

| provider | 端点 | 方法 | 认证 | 关键响应字段 |
| --- | --- | --- | --- | --- |
| deepseek | `https://api.deepseek.com/user/balance` | GET | `Authorization: Bearer <key>` | `is_available`、`balance_infos[].{currency,total_balance,topped_up_balance,granted_balance}`（金额是字符串） |
| commandcode | `https://api.commandcode.ai/alpha/billing/credits` | GET | `Authorization: Bearer <key>` | `credits.{monthlyCredits,purchasedCredits,freeCredits,belowThreshold}`，单位美元 |
| cline（余额） | `<baseUrl>/api/v1/users/me` → `/api/v1/organizations/<id>/balance` 或 `/api/v1/users/<id>/balance` | GET | `Authorization: Bearer <token>` | `balance`（整数，除以 1,000,000 得美元） |
| cline（ClinePass 窗口） | `<baseUrl>/api/v1/users/me/plan/usage-limits` | GET | 同上 | `limits[].{type,percentUsed,resetsAt}` |
| zai（Coding Plan 窗口） | `https://api.z.ai/api/monitor/usage/quota/limit` | GET | `Authorization: Bearer <key>` | `data.limits[].{type,usage,currentValue,percentage,nextResetTime,unit,number}` |
| droid / factory | `https://api.factory.ai/api/billing/limits` | GET | `Authorization: Bearer <key>` | `limits.standard`、`limits.core` 各自的 `fiveHour`/`weekly`/`monthly`（`usedPercent`、`windowEnd`） |

响应体默认按 `success`/`data` 或 `code`/`data` 解包；`is_available`、`isNull` 等缺失字段按各自默认值处理。

## 凭据来源

- deepseek：`Options.APIKey`。
- commandcode：`Options.APIKey` → `COMMAND_CODE_API_KEY` → `~/.commandcode/auth.json` 的 `apiKey`。
- cline：`CLINE_API_KEY` → `settings.auth.accessToken`（加 `workos:` 前缀）→ `settings.apiKey`；
  配置在 `$CLINE_DATA_DIR/settings/providers.json`、`~/.cline/data/settings/providers.json`
  或 `~/.cline/settings/providers.json`，`baseUrl` 缺省为 `https://api.cline.bot`。
- zai：`Options.APIKey`。
- droid：`Options.APIKey` → `FACTORY_API_KEY` → `$HomeDir/.factory/auth.json`（或 `credentials.json`）。

## 未实现的部分

- **Zai 重置卡**：`biz/customer-package-reset/list|use` 不实现——那是核销用户额度的写操作，
  本包只做只读读取。
- **Cline OAuth 刷新**：未实现 401/403 时用 `refreshToken` 换新令牌并写回 `providers.json`，
  本包直接报错，让调用方重新登录。
- **Factory 令牌刷新**：不实现，只把 settings 里的 key 作为 `FACTORY_API_KEY` 传给 CLI；
  `~/.factory` 是环境变量缺失时的兜底。
- **展示性字段**：Factory 的 `extraUsageBalanceCents` 文案（用作 planName）没有对应字段，
  `Window` 只保留 Label/UsedPercent/ResetsAt。
- **zai 余额**：z.ai 没有余额端点，`Read(zai)` 返回 `ErrUnsupported`。
- **货币格式化**：用一张小符号表（USD/CNY/EUR/GBP/JPY 等 14 种）；
  表外的 3 字母代码输出 `5.00 XYZ`。

## 测试

`*_test.go` 全部使用 `httptest.Server` 与 `testdata/*.json` 录制样本，
不访问真实网络，也不读真实 home 目录（HomeDir/Env 由测试注入）。
