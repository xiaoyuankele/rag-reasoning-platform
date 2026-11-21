# Repository Working Agreement

This file defines durable repository boundaries. Task-specific user instructions and an approved
Feature Control Card define the current scope; the skills under `.agents/skills/` define the
workflow used inside that scope.

## Sources of truth

- Use `docs/shared/` for cross-domain architecture and HTTP contracts.
- Use `docs/frontend/` for durable frontend facts and `docs/backend/` for durable backend facts.
- Use `contracts/` for versioned machine-readable cross-language protocols.
- Use current code and tests as evidence of implemented behavior. Plans and roadmaps are not proof
  that a capability exists.
- Keep daily progress, personal learning notes, temporary evidence, and conversation handoffs in
  the Git-ignored `chatgpt/` directory.
- Read only the documents relevant to the current change; do not load the full documentation tree
  for every task.

## Workflow roles

- `rag-feature-flow` owns cross-domain discovery, architecture decisions, scope, short contracts,
  handoffs, scope-change review, and final integration. It does not implement frontend or backend
  business code by default.
- `rag-frontend-dev` implements an approved frontend slice.
- `rag-backend-dev` implements an approved Go/Python backend slice.
- `rag-scale-test` designs and runs approved load, stress, soak, multi-instance, and fault-recovery
  tests. It treats production code as read-only and returns defects to the owning development skill.
- `rag-concept-brief` resolves a decision-relevant knowledge gap without implementing the feature.

Ordinary single-domain work may start directly in the corresponding development skill. Work that
changes shared behavior, crosses domains, or touches a protected surface must return to
`rag-feature-flow` before the first unapproved write.

Ordinary unit and feature tests stay with the frontend or backend development skill. Use
`rag-scale-test` only when the question concerns capacity, latency under load, long-running
stability, distributed coordination, concurrency correctness, or fault recovery.
Running an existing frontend check or backend L0-L5 gate by itself remains development-skill work;
scale testing starts when a new workload, topology, fault hypothesis, or controlled comparison must
be designed.

## Non-negotiable write boundary

- Read access may cross responsibility domains. Write authority does not.
- Before editing, identify the task base commit, the intended outcome, and the allowed paths.
- Responsibility-domain membership is only an upper bound. A task may modify only its explicit
  allowlist, not every file owned by that domain.
- If implementation needs a path outside the allowlist or a change to the frozen contract, stop
  writing. Inspect the impact read-only and report the evidence, exact paths, affected consumers,
  smallest alternative, compatibility impact, rollback, and verification plan.
- Do not make the out-of-scope change first and explain it afterward.
- Preserve user changes and unrelated worktree changes. Never revert or rewrite them to simplify a
  task.

## Responsibility domains

Frontend responsibility is bounded by:

- `web/**`
- `docs/frontend/**`

Frontend protected surfaces require explicit inclusion in the task scope: dependencies and lock
files, shared HTTP infrastructure, authentication, browser persistence, router-wide behavior,
global state, global styles, and build/tooling configuration.

Backend responsibility is bounded by:

- `backend/**`
- `ai/**`
- `docs/backend/**`

Backend protected surfaces require explicit inclusion in the task scope: migrations, dependency
files, public HTTP semantics, authentication and OwnerScope, privacy boundaries, leases/locks/
fencing/idempotency, task state machines, file storage and path safety, external providers,
deployment, startup/shutdown, and shared infrastructure.

Shared and repository-level protected surfaces include:

- `docs/shared/**`
- `contracts/**`
- root configuration and deployment files
- `scripts/**`

They are not forbidden, but they require a frozen cross-domain decision and an explicit owner.

Scale-testing responsibility is bounded by the exact approved harness, scenario, fixture, and
report paths. Production code, contracts, migrations, deployment configuration, dependencies,
shared test infrastructure, and formal performance documentation remain read-only unless the
Feature Control Card explicitly assigns a separate change owner.

## Implementation principles

- Prefer the smallest compatible change that satisfies observable acceptance criteria.
- Extend existing architecture and nearby patterns before introducing a new abstraction.
- Keep feature work separate from broad refactoring unless the refactor is proven necessary and is
  included in scope.
- Label verified facts, evidence-based inferences, and proposed future behavior distinctly.
- Record unresolved questions instead of inventing requirements or completion evidence.
- New external calls, credentials, paid services, production data, deployments, destructive data
  operations, and irreversible migrations always require explicit authorization.

## Verification and completion

- Test the changed boundary first, then run broader checks in proportion to risk.
- Reuse the repository's existing frontend commands and backend L0-L5 verification routes.
- Freeze the environment, topology, dataset, workload, metrics, correctness invariants, stop
  conditions, and cleanup plan before load or distributed testing. A local result is not a
  production SLA.
- State exactly which checks ran, which passed or failed, and which were skipped or unavailable.
- Do not call work complete when required evidence is missing, a contract mismatch remains, or the
  actual diff exceeds the approved allowlist.

## Cross-conversation workspace

For cross-domain, cross-conversation, or multi-day work, use:

```text
chatgpt/工作流/<task-id>/
├── control.md
├── frontend-handoff.md     # when needed
├── backend-handoff.md      # when needed
├── scale-test-handoff.md   # when needed
└── concepts.md             # when needed
```

`rag-feature-flow` owns `control.md`. Development, scale-test, and concept skills write only their named
handoff file when persistence is needed. These files coordinate active work; they do not replace
formal contracts or durable project documentation.

## 学习与架构说明

解释概念、架构、接口、依赖或数据流时，默认应用：

- `$adaptive-learning-style`
- `$concept-architecture-coach`

简单问题使用最短充分表达；复杂问题先给整体，再按需深入。
