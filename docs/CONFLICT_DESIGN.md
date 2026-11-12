# Conflict Policy

## Principle

Conflict detection is deterministic. `swarm_lead` only advises. It never changes
quorum, confirms a flag, submits, or deletes evidence.

## CTF

Different flags are valid. A conflict is only:

- the same flag has contradictory evidence/metadata;
- a challenge explicitly declares one flag but produces multiple candidates;
- the same finding fingerprint has contradictory conclusions.

### CTF key conflict

```text
quarantine disputed subject
do not submit disputed flag
keep non-conflicting results
continue task
Planner may create a verification Intent
```

CTF never waits for human review because of a conflict. `swarm_lead`'s
`needs_human` recommendation is downgraded to:

```text
quarantine_all + continue
```

If no further direction exists, normal planning-stall handling applies; it is not
a human conflict gate.

## Pentest

Key conflicts include:

- same action idempotency key with incompatible results;
- same target/asset with contradictory impact/evidence;
- exploit/change state is indeterminate.

### Pentest key conflict

```text
hold only the affected target/action
release or drain in-flight work
mark indeterminate
require human review before further action on that target
```

Other targets and Intents may continue. The whole task pauses only if all
remaining work is blocked on the same conflict.

## Matrix

| Mode | Non-key conflict | Key conflict |
|---|---|---|
| CTF | quarantine, continue | quarantine, Planner retry, no human |
| Pentest | quarantine/evidence review | needs_human for target/action |

## Current state

Implemented subset:

- cross-challenge same-flag conflict detection;
- `swarm_conflicts` persistence;
- optional `swarm_lead` conflict review;
- CTF downgrades `needs_human` to `quarantine_all` and continues;
- conflicts tab.

Implemented: same-subject/same-case finding observation inconsistency and manual
conflict resolution. Different strategies/cases with different observations are
evidence diversity, not conflicts. Still missing: Pentest action ledger/action conflicts.
