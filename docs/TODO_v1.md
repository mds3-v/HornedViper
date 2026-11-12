# TODO v1

> 快照：2026-10-23
> 范围：当前未完成、未验证、未产品化的工作。
> 已完成项不重复列出；Phase 3 的 CTF 核心路径已实现。

## 优先级

```text
P0 = 影响当前 Phase 3 正确性 / 可靠性
P1 = Phase 3 收尾必须补齐
P2 = Phase 4 或产品化增强
```

---

## P0 · 先保证 Phase 3 可信

### [x] 1. 真实 LLM 的 swarm / shard 端到端测试

当前只有 FakeRuntime 单测。

需要验证：

- [x] 2 lane swarm 真实模型执行
- [x] shard 资产分配和覆盖
- [x] quorum / min_success 合并
- [x] 失败 / 迟到 lane 进入 quarantine（FakeRuntime 单测）
- [x] Planner 收到聚合结果
- [x] trace 中能看到每个 lane 的调用（Lane 详情页）

Live tests:

```text
HV_LIVE_LLM=1 HV_CONFIG=$PWD/config.json go test ./internal/kernel -run 'TestLiveLLM(Shard|Swarm)' -v
```

已有测试：

```text
TestSwarmCreatesLanesAndMerges
TestShardCreatesDisjointLanesAndMerges
TestSwarmSubmitsOnlyConfirmedFlagsFromMultiFlagFinding
TestFailedSwarmLaneIsQuarantined
```

### [x] 2. shard 显式 asset_ids 的任务归属校验

现状：

```json
"asset_ids": [1, 2, 3]
```

可能引用其他任务的全局资产。

需要：

- [x] 校验资产属于当前 task 的 anchored assets
- [x] 校验 asset scope
- [x] out_of_scope 资产拒绝或隔离
- [x] 测试跨任务 asset id 被拒绝

### [x] 3. 显式 `CTFLanePolicy`

现状：

```text
swarm / shard 默认就是 CTF 语义
没有 task.Mode 分流
```

需要：

- [x] 抽出 `CTFLanePolicy`
- [x] `task.Mode` 路由
- [x] 为后续 Pentest Profile 留接口
- [x] 保持当前 CTF 行为不变
- [ ] 完整 lane planner / supervisor 拆分仍是后续工作

---

## P1 · Phase 3 收尾

### [~] 4. `swarm_lead` 运行时（lane plan 已完成；冲突裁决待补）

目前只有 Agent 配置种子：

```text
swarm_lead · 蜂群指挥
```

还缺：

- [x] Planner 选择 swarm 后，Engine 总是调用 swarm_lead
- [x] Planner strategies 作为 strategy_hints 传给 lead
- [x] lead 仍是唯一 lane plan 作者；hints 不作为最终 plan
- [x] 无默认 strategy；lead 返回不完整则 block
- [x] `swarm_lead` LLM smoke test
- [x] 连续 planning failure 后任务进入 paused / planning stalled
- [x] swarm_runs.planned_by 记录 swarm_lead
- [x] 跨 challenge 同 flag 冲突检测
- [x] swarm_lead 冲突建议（keep_all / quarantine_all / needs_human）
- [x] CTF 冲突自动 quarantine，不等待人工
- [x] swarm_conflicts 落库 + 冲突 Tab
- [x] finding 级同 subject + 同 case 矛盾检测
- [ ] Pentest action ledger / action 冲突
- [x] 冲突 Tab 手动裁决（保留 / 隔离 / 人工复核）
- [x] 真实 LLM conflict review smoke test

### [ ] 5. Quarantine 的读取和提升

已有：

- [x] `quarantine_items`
- [x] 失败 / 迟到 / 未确认 flag 写入
- [x] Planner packet digest
- [x] 隔离 Tab
- [x] 分页 10 条

还缺：

- [x] Planner `list_quarantine` 工具
- [x] Planner `read_quarantine(id)` 工具
- [x] 读取限长
- [x] promote / dismiss UI
- [x] promote 生成 quarantine evidence artifact
- [x] promote 不自动 confirmed / 不提交 flag
- [ ] promote 审批流程
- [ ] 大 payload 写入 artifacts
- [ ] 过期清理策略
- [ ] 同一 flag 证据矛盾复核

### [x] 6. lane 级详情 / trace

当前只有 lane 汇总表。

需要：

- [x] 从 lane 跳到该 lane 的模型调用
- [x] 按 lane 过滤模型调用 / 活动
- [x] 查看 lane 的原始 prompt / brief
- [x] 查看 lane 的 quarantine
- [x] 查看 lane artifacts 文件列表

### [x] 7. 计划页展示 lane 规划

当前只显示 `execution_mode`。

需要显示：

- [x] lanes
- [x] quorum / min_success
- [x] lane 状态
- [x] strategy / lane brief 详情
- [x] lane 资产数量 / shard 覆盖

### [ ] 8. 容器槽位与 MaxWorkers 联动

需要：

- [x] CTF 容器槽位计数（MaxContainers admission）
- [x] lane 调度前预留槽位
- [ ] 容器启动失败时 lane 回退（尚无容器生命周期）
- [x] 不让 Worker 空转等待容器
- [x] UI 展示槽位占用（计划页 Active/Max）

### [ ] 9. running lane 的等待 / 监督 / drain

当前达到 quorum 后：

```text
queued lane 取消
running lane 继续跑，结果不合并
```

需要：

- [x] 最多 5 分钟等待窗口（Config.DrainWindow，生产 5m）
- [x] late result quarantine
- [x] 避免重复合并
- [ ] lane telemetry / checkpoint
- [~] 到期 soft-cancel + merge_now（确定性 supervisor）
- [ ] continue 决策 / 根据 lane telemetry 继续等待
- [x] soft cancel（drain deadline 取消 Work context）
- [ ] 当前工具 drain（runtime 不感知工具边界）
- [ ] 释放容器和 lease（admission slot 只释放计数）

---

## P2 · Phase 4 Lane 生命周期

这些已明确延期到 Phase 4。

### [ ] 10. LaneSpec + WorkAttempt 模型

从：

```text
Work(lane_id)
```

升级为：

```text
LaneSpec
  └─ WorkAttempt(s)
```

字段：

- lane strategy / brief
- scope
- attempt_no
- worker_id
- status
- audit

### [ ] 11. lane 失败重试

- [ ] 重试策略
- [ ] 最大重试次数
- [ ] 退避
- [ ] 只重试失败 lane
- [ ] 不重复成功 lane

### [ ] 12. lane 暂停 / 恢复

- [ ] 可暂停状态
- [ ] 上下文 / 工具状态保存
- [ ] resume 协议
- [ ] lease 释放与恢复

### [ ] 13. lane 多次 attempt

- [ ] attempt 历史
- [ ] 每次 attempt 独立结果
- [ ] 合并时选择有效 attempt
- [ ] attempt 级取消

### [ ] 14. lane 级审计

- [ ] 谁创建 lane
- [ ] 谁执行
- [ ] 每次 attempt 的决策
- [ ] 取消 / 重试原因
- [ ] quarantine 来源
- [ ] UI 可查

---

## P2 · Pentest Profile

当前还没有 Pentest lane 策略。

### [ ] 15. effect 模型

- [ ] observe
- [ ] verify
- [ ] exploit
- [ ] change

### [ ] 16. scope / lock

- [ ] lane_scope
- [ ] 工具级 scope 校验
- [ ] shared / exclusive lock
- [ ] target lease
- [ ] scope 外阻断

### [ ] 17. 副作用控制

- [ ] exploit 单写者
- [ ] change 串行
- [ ] approval
- [ ] action idempotency key
- [ ] before / after
- [ ] rollback
- [ ] action ledger

### [ ] 18. Pentest Merger / Supervisor

- [ ] 证据复现
- [ ] finding 合并
- [ ] action ledger 合并
- [ ] 风险优先 supervisor
- [ ] 不把 quorum 用于有副作用操作

---

## P2 · 平台与工具缺口

### CTFd / Benchmark

- [x] CTFd 自动拉题
- [ ] CTFd 容器启动 / 停止
- [ ] benchmark 容器生命周期
- [ ] rCTF / 其他平台

### Worker 工具

- [ ] 任务 Worker 增加 WebSearch
- [ ] 任务 Worker 增加 read_image
- [ ] 工具级 scope 拦截

### 沙箱

- [ ] workspace-write 变成 OS 级沙箱
- [ ] Bash / Python 文件系统限制
- [ ] 容器化工具执行

### 运行与可观测性

- [ ] 后台 job 列表跨重启
- [ ] 自动 CTF 后台定时轮询
- [ ] 日志页面显示更多历史
- [ ] Agent `tools_json` 完全驱动运行时
- [ ] 生产级鉴权
- [ ] 多 Agent UI 编排

---

## Phase 3 完成标准

Phase 3 可以标记完成前，至少需要：

- [x] P0 三项完成
- [ ] `swarm_lead` 可用或明确移除
- [ ] quarantine 可读、可提升、可审计
- [ ] lane 可追踪
- [ ] 真实 LLM swarm/shard E2E 通过
- [ ] 容器槽位不会超卖
- [ ] 文档状态与代码一致

## 备注

当前已实现且不在本 TODO 内：

- CTF swarm / shard 核心
- lane brief
- per-flag quorum / 多 flag 独立提交
- queued lane 提前取消
- quarantine 存储 / digest / UI / 分页
- shard 覆盖汇总
- 计划页 execution_mode

## Budget semantics

- `max_minutes` no longer imposes a deadline.
- `hard_max_minutes` is the optional whole-Task hard deadline.
- Direct HTTP flag submissions are deduplicated and recorded when accepted.
