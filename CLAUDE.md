# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What helios is

helios is the **orchestrator** in the [Lumos family](#lumos-family). It sits
between an upstream detection system (hack-detector) and an analysis engine
(LumosKit), runs the state machine that turns a detected suspicious transaction
into a verified PoC + RCA bundle handed off to downstream agents, and starts
verified-only product side effects such as GitHub publish and Pre-Lumos
importer JSON generation.

helios is **not** a generic workflow platform, not a security scanner, and does
not perform detection or analysis itself. Those responsibilities belong to its
two siblings.

## Lumos family

helios is one of three sibling repos at `/home/wiimdy/lumos/` with deliberately
narrow, non-overlapping responsibilities:

| Repo | Role | Canonical location |
| --- | --- | --- |
| `hack-detector` | Upstream detection — finds suspicious tx hashes on-chain | `github.com/UPside-Lumos-V2/hack-detector` |
| **`helios`** | **Orchestrator — receives signals, dispatches engine runs, manages state, records product side effects, hands off to downstream agents** | this repo |
| `lumoskit` | Stateless CLI engine — turns a tx hash into a verified PoC + product bundle | sibling at `../lumoskit/` |

The boundary between helios and lumoskit is fixed by
**`../lumoskit/docs/adr/0018-lumoskit-as-engine.md`** — read that ADR before
adding orchestration features. It defines what lumoskit will and will not do,
which inversely defines what helios is responsible for.

## What lives where (and what does not)

Things that belong in **helios**:

- State machine for incident lifecycle (queued → running → done → handed-off, or running → failed for engine errors)
- Subprocess dispatch of `bin/lumoskit`
- Concurrency control across multiple simultaneous cases
- Retry policy / idempotency / dedup of repeated tx hashes
- Notification delivery (operator webhook and Telegram; Slack/Discord/email go through webhook receivers)
- Case tracking metadata store and search
- Routing of `outputs/<case>/summary.json` to downstream consumers
- Verified-only GitHub product publish for `PoC.t.sol` and `Report.md` as `README.md`
- Verified-only Pre-Lumos Agent SDK sidecar orchestration using the vendored `skills/pre-lumos` bundle

Things that belong in **lumoskit** (do not duplicate here):

- Trace acquisition, CEFG, localization, lifting, PoC synthesis, agent_poc gate
- The engine pipeline itself; helios only invokes it as a subprocess

Things that belong in **hack-detector** (do not duplicate here):

- On-chain monitoring, suspicious tx detection
- Anything that decides "is this an incident worth analysing"

If a proposal asks helios to do detection, redirect to hack-detector. If it
asks helios to do trace/PoC analysis, redirect to lumoskit. If it asks
lumoskit to do orchestration, redirect here.

## How helios calls lumoskit (engine contract)

Per `../lumoskit/docs/adr/0018-lumoskit-as-engine.md`, lumoskit exposes a
stable subprocess engine contract. helios consumes it like this:

```text
1. helios picks a unique --output-root for the case (e.g. outputs/<case-id>/).
2. helios spawns `bin/lumoskit --tx <hash> --chain <label> --output-root <path>` as a one-shot subprocess.
3. helios waits on the subprocess. Exit code 0 = success, 1 = anything else.
4. helios reads `<output-root>/summary.json`:
   - status: "pass" | "partial" | "fail"
   - poc.status: "verified" | "unverified" | "missing"
   - failure.kind: present when status != "pass"
   - For engine errors: failure.kind == "engine_error" and failure.message has the original error string.
5. For `outcome=verified`, helios may publish product artifacts to GitHub and run the Pre-Lumos sidecar, recording `github_publish` / `pre_lumos_sync` audit events.
6. helios routes the case (and `summary.json` URL/path) to downstream agents.
```

Notes:

- **No --rpc-url flag**. RPC is resolved by lumoskit from env at startup
  (`CEFG_LIVE_RPC_URL` / `RPC_URL` / `ETH_RPC_URL`, or `ALCHEMY_API_KEY` plus
  `--chain`). helios sets env, not flags.
- **Caller-unique --output-root is mandatory.** lumoskit takes no file lock;
  collisions on the same root are caller error.
- **summary.json is always written**, including failure paths. helios can rely
  on reading it after subprocess exit.
- **Do not embed lumoskit as a library.** The engine contract is the subprocess
  surface; in-process embedding violates the contract boundary.

## Implementation choices

The v1 implementation contract is now captured in `seeds/v1.yaml`.

- Go single-process HTTP service.
- SQLite durable state.
- Standard-library HTTP stack.
- SQLite-backed FIFO queue.
- Hand-rolled state machine with every state transition recorded in `case_events`.
- Direct downstream webhooks with bounded retry.
- Optional verified-case GitHub publish and Pre-Lumos importer JSON side effects.
- Single systemd/container process with persistent DB and output roots.

When changing behavior, update `seeds/v1.yaml`, `README.md`, and
`docs/architecture/overview.md` together so the workflow contract stays aligned
with the implementation.

## Useful pointers (cross-repo)

- `../lumoskit/docs/product.md` — LumosKit product charter. helios serves the same audience (audit firms doing post-incident forensics) — so helios features should pass the same audience-fit test on the workflow side.
- `../lumoskit/docs/adr/0018-lumoskit-as-engine.md` — the engine contract. Always check this before assuming what lumoskit will do.
- `../lumoskit/docs/architecture/README.md` — lumoskit per-stage outputs and `outputs/<case>/summary.json` schema reference.
- `../hack-detector/` (local clone of `UPside-Lumos-V2/hack-detector`) — upstream signal shape and integration target.

## What this file is not

This file captures durable architectural facts derived from cross-repo
interviews already completed. It is intentionally short and skips
implementation detail. For current behavior, start from:

- `README.md` for run/config/docs entry points.
- `seeds/v1.yaml` for the durable workflow contract.
- `docs/architecture/overview.md` for operator workflow diagrams.

<!-- ooo:START -->
<!-- ooo:VERSION:0.38.2 -->
# Ouroboros — Specification-First AI Development

> Before telling AI what to build, define what should be built.
> As Socrates asked 2,500 years ago — "What do you truly know?"
> Ouroboros turns that question into an evolutionary AI workflow engine.

Most AI coding fails at the input, not the output. Ouroboros fixes this by
**exposing hidden assumptions before any code is written**.

1. **Socratic Clarity** — Question until ambiguity ≤ 0.2
2. **Ontological Precision** — Solve the root problem, not symptoms
3. **Evolutionary Loops** — Each evaluation cycle feeds back into better specs

```
Interview → Seed → Execute → Evaluate
    ↑                           ↓
    └─── Evolutionary Loop ─────┘
```

## ooo Commands

Each command loads its agent/MCP on-demand. Details in each skill file.

| Command | Loads |
|---------|-------|
| `ooo` | — |
| `ooo interview` | `ouroboros:socratic-interviewer` |
| `ooo seed` | `ouroboros:seed-architect` |
| `ooo run` | MCP required |
| `ooo evolve` | MCP: `evolve_step` |
| `ooo evaluate` | `ouroboros:evaluator` |
| `ooo unstuck` | `ouroboros:{persona}` |
| `ooo status` | MCP: `session_status` |
| `ooo setup` | — |
| `ooo help` | — |

## Agents

Loaded on-demand — not preloaded.

**Core**: socratic-interviewer, ontologist, seed-architect, evaluator,
wonder, reflect, advocate, contrarian, judge
**Support**: hacker, simplifier, researcher, architect
<!-- ooo:END -->
