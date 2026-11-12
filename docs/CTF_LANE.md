# CTF Lane Design

Status: frozen for Phase 3 CTF work.

## Modes

### single

```text
1 Intent = 1 Work
```

### swarm

Same Intent, multiple strategy lanes.

```text
Intent c-03
  lane-1 source-review
  lane-2 dynamic-exploit
  lane-3 differential-test
```

- lanes share `intent_id`
- independent lane_id, workdir, session, context
- lane receives one strategy + concrete lane brief
- flags confirmed by quorum
- successful lanes >= quorum required
- queued lanes cancelled after quorum
- running lanes are not hard-cancelled in the current implementation; late results are ignored

Defaults:

```text
lanes = 3 (max 6)
quorum = 2
```

### shard

Same Intent, disjoint asset subsets.

```text
Intent batch
  shard-1 assets A
  shard-2 assets B
```

- group by parent/host
- one asset only in one shard
- no flag quorum across shards
- `min_success` controls completion
- default `min_success = lanes`
- queued lanes cancelled after min_success

## Merge

```text
assets   canonical-key dedupe
facts    summary + evidence dedupe
findings title + evidence dedupe
flags    swarm: lane-hit quorum; shard: per-shard
```

Only merged confirmed flags reach SubmitService.

## Planner

Planner may emit multiple Intents in one planner response. Each parallel Intent maps to N lane Works; the Planner does not create one Intent per lane. It does not see lane events, and receives one aggregated result per merged parallel Intent.

## Current implementation boundary

`swarm` and `shard` currently carry CTF semantics by default. There is no explicit
`CTFLanePolicy` type or `task.Mode == ctf` check yet. Pentest effects, scope,
locks, approvals and action ledger are not part of this frozen CTF design.

## Deferred to Phase 4

The current model is one lane = one Work. The following lane lifecycle features are
deliberately moved to Phase 4 and are not part of the Phase 3 CTF lane contract:

1. lane 失败重试
2. lane 暂停 / 恢复
3. lane 多次 attempt
4. lane 级审计

Phase 4 will introduce `LaneSpec` plus `WorkAttempt` instead of treating a lane as
a single Work row.

## Multi-flag tasks

Different flags are not conflicts. A CTF task or challenge may require collecting
multiple flags. Quorum is evaluated per flag:

```text
flag A: lane hits 1 -> not confirmed (quorum=2)
flag B: lane hits 2 -> confirmed
flag C: lane hits 3 -> confirmed
```

A finding may contain more than one flag. It is recorded once, but each flag is
submitted only if that flag itself passes quorum. `task_flags` already allows many
flags per task, with uniqueness on `(task_id, flag_hash)`.

For collection-style tasks the Planner may choose a lower flag quorum; the lane
success quorum and the per-flag confirmation threshold should not be assumed to
be the same number forever.

## Merge metrics

`confirmed_flags` counts only flags whose lane-hit count reached quorum.
`flags_seen` counts all distinct flags observed. A finding can contain multiple
flags; only confirmed flags are submitted individually.

## Planner hints

Planner may provide `budget.strategies`; the host converts them to advisory
`strategy_hints` for swarm_lead. swarm_lead remains the sole author of the final
lane plan.
