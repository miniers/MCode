# 04 编排（Swarm）

编排让主代理（lead）把工作派给子代理（bee），子代理在独立的工作副本里干活，完成后合并并汇报。

代码在 `internal/swarm`，和内核的接线在 `internal/app/swarm_wiring.go`。

## 派发

主代理有两种派发方式：

- 在回复里写 ` ```swarm ` JSON 代码块。字段：`task`、`prompt`，可选 `bee`、`tier`、`skills`、`project`、`replaces`。
- 调 `swarm_delegate` 工具。需要开启 `orchestration.delegate_tool`，默认关闭。

`StreamParser` 流式解析代码块，`swarmStreamDispatcher` 负责派发（`feed`、`settleTurn`、`roundLimit`、`entryCap`）。

`Manager` 的派发入口：`Dispatch`、`DispatchFromReply`、`DispatchSingleEntry`。

## 工作模式

每个会话可以单独设置，取值在 `internal/swarm/mode.go`：

| 模式 | 含义 |
|---|---|
| `inherited` | 跟随全局设置，会话未设置时的默认值 |
| `collaborative` | 主代理规划并委派大任务，小改动可亲自执行 |
| `leadPlansOnly` | 主代理只规划不动手，所有改动都派给 bee |
| `alwaysBees` | 每条消息直接派给 bee，绕过主代理 |

「关闭」不是模式，由 `Manager.State(threadID).Enabled` 决定。全局开关是 `orchestration.always_bees` 和 `orchestration.lead_plans_only`。`EffectiveMode` 解析最终模式。

提示词在 `internal/swarm/prompts.go`：`LeadPolicy`、`LeadNoticePolicy`、`NativeSubagentRule`、`DelegateToolRule`。它们通过 `policy.Render` 注入，见 [02-session-runtime.md](02-session-runtime.md#提示词注入)。

## 子代理生命周期

`Manager`（`manager.go`）通过 `Tick`（每 2 秒）驱动：

1. **启动**（`startSingleBee`）：解析 Provider、模型和推理强度，准备工作副本，创建子会话。
2. **排队**（`processQueue`）：超过并发上限的任务排队。
3. **运行**：状态 `running`。可用 `StopBee`、`StopAll` 停止。
4. **结束**：`completed`、`failed`、`stopped`。超时默认 40 分钟（`bee_timeout_minutes`）。
5. **合并**：`prepareMerge`、`applyMerge`。
6. **汇报**：`checkReports`。

解析规则：

- Provider 和模型：派发指令未指定时用配对或档位，再回退到 `orchestration.default_provider`、`default_model`。
- 推理强度：派发指令、配对、档位都没指定，且实际用的就是默认 Provider 和模型时，才回退到 `default_effort`。这个规则和 `TierPreview` 共用。
- 权限模式：宿主指定的优先，否则继承主会话。子代理无人值守，不能卡在审批上，所以只读档也继承运行模式，只读约束由提示词和完成后的 `ReadOnlyViolation` 检查保证。

其他限制：

| 配置 | 含义 |
|---|---|
| `max_bees` | 并发子代理数，1 到 8，0 表示不限 |
| `max_rounds` | 主会话最多可调起子代理的轮次，0 表示不限，上限 100 |
| `bee_timeout_minutes` | 子代理超时，默认 40 |
| `request_timeout_minutes` | 单次用户请求的编排总预算，默认 180，范围 30 到 720 |
| `auto_forward_question_seconds` | 子代理提问无人回答多久后转给主代理，0 表示关闭 |

## 工作副本

- `workspace_mode`：`worktree` 或 `shared`。写入白名单不为空的 bee 和只读 bee 不能走共享工作区，否则无法过滤白名单外的改动。
- 非 git 项目用影子仓库（`ShadowRepo`，放在 `shadow-git/`），不可用时退到 `SharedNoGit`。
- 工作副本放在 `swarm-worktrees/`。
- `sweep.go` 的 `SweepOrphanCopies` 清理孤儿副本。`reclaimBeeCopies` 回收。
- 重试（`RetryBee`）会接管上一个 bee 失败时保留的副本，跳过基线检查点和 `worktree add`，启动失败也保留现场。

## 合并

`orchestration.review_mode` 决定 bee 结束、补丁非空后怎么处理，默认 `off`。这是运行期设置，改完立即生效，不用重启。配置版本 9 的迁移把旧的布尔键 `review_before_merge` 转成三档：`true` → `human`，`false` 或缺失 → `off`，迁移后删除旧键。

| review_mode | 行为 |
|---|---|
| `off` | 汇报后自动合入（`mergeBee`）。默认值 |
| `human` | 补丁暂存为 `PendingMerge`（`ReviewState=needs_human`），等用户在编排面板批准（`ApproveMerge`）或放弃（`DiscardMerge`）。批准只应用展示的补丁快照 |
| `ai` | 为每个有改动的 bee 起只读审查会话，结论出来前不合入，见下方「AI 审查」 |

进入待人工审查（`PendingMerge` 且不在审查中）时会产生一条「有改动待审查」的注意力提醒，桌面端会话头显示「待审查 N」。

- `integratePatch` 在对象层整合 bee 工作区和主工作区的最新内容。
- `handbackConflict` 把冲突交回 bee 的 worktree，并启动接手的新 bee 解决冲突。
- `deferMerge` 记录本次不落地的等待起点。等待超时后把补丁另存为 `.patch` 文件，标记 bee 未合入，放弃本次落地。
- `maybeFireBatchLanded` 在一批全部落地后触发回调。

### AI 审查

`ai` 档为每个有改动的 bee 起一个只读审查会话。会话占并发名额，但不占 bee 配额。审查会话的输入包括：

- bee 的任务说明（`Task`、`Prompt`）和汇报总结（`Summary`），以及改动文件列表。
- 补丁快照文件 `swarm-reviews/<leadID>-<index>.patch`，放在数据目录下；不要和 `swarm-patches/` 的落地补丁混用。
- bee 的工作副本，以 `readonly` 模式挂给审查会话。

提示词要求审查会话只读：不改文件、不执行改状态的 git 命令、不派发子代理。回复里必须带唯一一个 `swarm-review` 代码块，块体是 JSON：

```swarm-review
{"verdict": "approve", "reason": "一句话原因"}
```

`verdict` 只能是 `approve`、`reject`、`needs_human`。`reject` 必须带非空原因，缺原因时降级为 `needs_human`；没有代码块、JSON 非法或取值未知都按解析失败处理。

结论处理：

- `approve`：只合入审查过的那份快照。合入前重新比对补丁文件的 SHA 与 bee 当前的 `MergePatch`，不一致就转人工，绝不合入未审查的内容。与主工作区冲突时按自动合并的路径交回新 bee 处理，交回失败则转人工；主工作区还没落定时本次不合入，下一次轮询重试。
- `reject`：不合入，保留补丁（MergePatch 与 swarm-reviews 下的补丁文件），汇报后按放弃的规则回收工作副本，不自动返工。汇报里写明审查意见和补丁路径，由主代理决定返工还是放弃。
- `needs_human`：转人工，补丁继续待审，等用户批准或放弃。

以下情况一律转人工（`ReviewState=needs_human`，`PendingMerge` 保持）：审查超时（10 分钟）、会话失败、审查会话启动失败、结论解析失败、审查会话改动了工作副本（工作副本指纹变化）、审查快照变化，以及 MCode 重启时审查仍在进行。

审查模型的解析顺序在发起审查的那一刻执行，不缓存：

1. 显式配置的 `review_provider`、`review_model`、`review_effort` 优先；只写了其中一项时，其余项按子代理默认值补全。
2. 没有显式配置时跟随主会话当前的 provider、model、effort。这由 `LeadRuntimeSource` 接口提供，实现是 `internal/remote/kanban_driver_review.go` 的 `MemoryBackend.LeadRuntime`。
3. 读不到主会话时回落到子代理默认模型（`resolveBeeRuntime`）。

### 自动合并流水线

`automerge.go` 分 7 个阶段，可在重启后续做（`ResumeBatch`）：

`Gathering → Committing → Describing → Pushing → Opening → Merging → Syncing`

只有开启 `auto_push` 才做远端操作（push、用 `gh`/`glab` 开 PR）。`auto_merge` 只做本地提交和 squash 合并。

## 汇报

`checkReports` 的规则比较细，是这个子系统最容易出问题的地方：

- 只要还在派发、还有排队任务或还有 bee 在跑，整批都算工作中，全部落定后一次性汇报。
- `ai` 和 `human` 两档：同一主会话里只要还有 bee 在审查中（`ReviewRunning`）或待人工审查（`PendingMerge`），这一批就先不汇报，也不回调 `OnBeesIdle`，跟进队列不出队；全部有结论后一次性汇报。`off` 档 bee 一结束就汇报。
- 合并结果写成「已合入（审查通过）」「审查未通过」（附审查意见和补丁路径）或「已放弃合并」。
- 审查模式下，主代理的提示词会改为说明汇报在审查之后才到；审查未通过或已放弃的改动不在主工作区，需要返工就派新 bee。
- 跟进队列里的系统条目（子代理汇报、提问转交、编排通知）是只读的，不能编辑或撤回，后端也会拒绝这两个操作。
- 主代理还在执行时不投递汇报。投递方只会把它排进跟进队列，而那个队列可能不再被处理，bee 又已被标记为已汇报，汇报就丢了。所以等主代理空闲后的下一个 tick 再发。
- 主代理上一轮被用户停掉时，汇报暂扣到用户下一句话一起走（`reportsHeld`）。
- 超长总结先存档全文再截断（`report_archive.go`），主代理用 `swarm_report_get` 取回。
- 查询 `SessionState` 要在 `m.mu` 之外，因为可能反向取后端锁。

汇报消息带前缀 `[编排] 子代理汇报：`，兼容旧的 `[Hydra]`。

`credibility.go` 核验汇报里的引用是否可信。

`answer.go` 处理 `swarm-answer` 代码块：子会话的提问转给主代理，主代理用这个块回答。

## 重启恢复

状态保存在 `mcode.db`（`hydra_leads` 表）。重启后 `Restore` 恢复，仍在运行的 bee 标为 `Interrupted`，不自动合并半成品。用户可选 `ResumeInterrupted`、`RetryBee`、`DismissInterrupted`。

## 档位与配对

- `tiers.go`：档位定义（`orchestration.tiers`、`default_tier`），RPC 为 `swarm.tiers.*`。
- `pairs.go`：主代理和子代理的模型配对（`orchestration.pairs`）。
- `profile.go`：导入旧 profile。

## 改动提示

- SQLite 的物理表名和列名仍是旧名（`hydra_leads`、`heads_json`、`head_tiers`、`hydra_pair_id`、`hydra_mode`），不要试图直接重命名，迁移规则见 [10-store-config.md](10-store-config.md)。
- 修改提示词要递增 `policy.Version`。
- 派发和汇报逻辑的测试集中在 `internal/swarm` 和 `internal/app`，改动后跑这两个包。
