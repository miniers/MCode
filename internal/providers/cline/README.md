# internal/providers/cline

Cline 的模型目录与价格读取，供 provider 注册、设置页与模型库展示接线。

- `cline.go`：`SeedModels` 返回内置的 Cline 初始模型列表（4 项，默认 `cline-pass/deepseek-v4.1-flash`）；`CatalogModels` 在该列表上合并 models.dev 的 `cline-pass` 价格（`ParsePricing` / `FetchPricing` / `CachedPricing`，带 10 分钟内存缓存；`MergePricing` 只补模型里为 0 的字段），价格拉取失败时仍返回初始列表并返回错误。`Plan` 区分按量付费（`cline`）与订阅（`cline-pass`）。
- `testdata/`：`models_dev_cline.json`（models.dev 片段）；`cline_test.go` 用 `httptest.Server` 覆盖初始列表、价格解析、目录合并、拉取失败降级与缓存，不访问真实网络。

尚未实现的部分：模型库展示（provider 注册与设置页已完成，改动需重启生效）；ACP 动态探测模型未实现，ClinePass 的订阅状态端点 `/api/v1/users/me/plan` 未实现（对应的用量读 `/api/v1/users/me/plan` 与 `/api/v1/users/me/plan/usage-limits`，已在 `internal/providers/credits/cline.go` 实现）。

差异说明：Cline 没有 HTTP 模型列表端点，模型目录改由初始列表加 models.dev 价格构成，不再请求 `/api/v1/models`。
