# models.dev 快照来源说明

本目录的 `snapshot.json` 是 models.dev 模型目录的裁剪副本，通过 `go:embed` 打进二进制，
作为离线基线数据（推理档位、上下文上限、价格、输入/输出模态等）。

## 数据来源

- 下载地址：`https://models.dev/api.json`（完整目录 JSON）。
  该地址写死在 `scripts/fetch_models_dev_snapshot.sh` 的 `MODELS_DEV_URL` 默认值里，
  可用同名环境变量指向镜像；脚本会在下载前打印实际使用的地址。
- 上游项目：models.dev（GitHub 仓库 `anomalyco/models.dev`，旧地址 `sst/models.dev` 会重定向到该仓库），
  站点为 <https://models.dev>。各 provider 的模型条目由上游从其官方文档整理发布。
- 上游许可证：MIT License（2026-10-22 通过 GitHub API 查询 `anomalyco/models.dev` 的 license 字段确认）。
  若之后需要正式分发该数据，请以上游仓库的 LICENSE 文件为准再核对一次。

## 快照如何生成

1. 运行 `scripts/fetch_models_dev_snapshot.sh`（默认不会在打包时自动执行，
   只有在 `MCODE_REFRESH_SNAPSHOT=1` 时两个打包脚本才会调用它）。
2. 脚本下载 `api.json`，用内联 Python 裁剪：只保留 `internal/modelsdev.Providers`
   列出的 provider（anthropic、openai、google、cline-pass）与目录需要的字段，
   去掉空值后以紧凑 JSON 覆盖写入 `internal/modelsdev/snapshot.json`。
   下载或裁剪失败时保留原快照，不写半截数据。
3. 人工审阅 `internal/modelsdev/snapshot.json` 的 diff（新增/下线模型、上下文与价格变化），
   确认合理后再随代码一起提交。

当前快照包含 4 个 provider、127 个模型条目。
