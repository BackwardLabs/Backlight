# Pre-Lumos Normalization Rules

Use these rule IDs when validating, repairing, and revalidating rows for the Lumos JSON importer contract defined by this skill.

## Required Field Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `REQ-001` | blocker | contract-validator | `slug` must exist and remain non-empty after trimming |
| `REQ-002` | blocker | contract-validator | `name` must exist and remain non-empty after trimming |
| `REQ-003` | blocker | contract-validator | `hackedAt` must exist |
| `REQ-004` | blocker | contract-validator | `chains` must exist and contain at least one value |
| `REQ-005` | blocker | contract-validator | `amount` must exist and remain numeric |
| `REQ-006` | blocker | contract-validator | `category` must exist and remain non-empty after trimming |

`category2` may be `null`.

## Status Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `STS-001` | blocker | rule-validator | Only `yes`, `no`, `rugged`, or `null` survive as statuses |
| `STS-002` | repairable | rule-validator | Normalize shorthand or case drift to the canonical status set |
| `STS-003` | repairable | rule-validator | `bankrupt` is unsupported in the importer contract and must not survive unchanged |

## Canonical Value Preference

### `category`

Prefer:

- `Rugpull`
- `Fraud`
- `Misc.`
- `Malicious Governance Proposal`
- `Government Sanctions`
- `Compiler Vulnerability`
- `Stablecoin Depeg`
- `Source Code Vulnerability`
- `Contract Vulnerability`
- `Control Hijacking`
- `Circulating Supply`
- `Unknown`

### `category2`

Prefer:

- `Perpetual`
- `Yield`
- `Lending`
- `Staking`
- `CEX`
- `Synthetics`
- `DEX`
- `Token`
- `Unknown`

### `scope`

Normalize to:

- `In Scope`
- `Out of Scope`
- `null`

## Formatting Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `FMT-001` | repairable | rule-validator | `slug` must be lowercase, URL-safe, and hyphenated |
| `FMT-002` | repairable | rule-validator | `twitter` must be handle-only, never a full URL and never `@handle` |
| `FMT-003` | repairable | rule-validator | `website` must be an absolute URL when present |
| `FMT-004` | repairable | rule-validator | `logoImage` must be an absolute image URL when present |
| `FMT-005` | repairable | rule-validator | date-like fields should normalize to `YYYY-MM-DD` |
| `FMT-006` | blocker | contract-validator | `amount` must remain numeric only |

## Null and Empty Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `NULL-001` | repairable | rule-validator | Normalize empty strings and whitespace-only values to `null` |
| `NULL-002` | repairable | rule-validator | Prefer `fund: null` over `fund: {}` |
| `NULL-003` | repairable | rule-validator | Do not emit empty audit objects |

## Lookup and Canonical Reuse Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `LOOKUP-001` | repairable | rule-validator | Trim lookup-backed values before reuse or creation |
| `LOOKUP-002` | warning | rule-validator | Prefer canonical reuse over vocabulary expansion for lookup-backed fields |
| `LOOKUP-003` | repairable | rule-validator | Deduplicate `chains` after normalization |

## Audit Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `AUD-001` | repairable | rule-validator | Never emit an empty audit object |
| `AUD-002` | repairable | rule-validator | Drop audit rows that have no meaningful `firm`, `timestamp/date`, `reportUrl`, or scope signal |
| `AUD-003` | repairable | rule-validator | `timestamp` is the primary audit date field; `date` is fallback-only input |
| `AUD-004` | warning | rule-validator | `postAudits[].scope` is not operationally important in the importer contract defined by this skill |

## Fund Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `FUND-001` | repairable | rule-validator | Do not repeat the same destination type across `fund.destinations` and `fund.destinations2[].name` |
| `FUND-002` | warning | rule-validator | Blank `fund.links[].value` is noisy and may collapse to the URL |
| `FUND-003` | repairable | rule-validator | Deduplicate `fund.destinations`, `fund.destinations2`, and `fund.links` when evidence is unchanged |

## Compensation and Postmortem Rules

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `COMP-001` | repairable | rule-validator | `compensation.detail` should not survive without a usable non-`rugged` status |

## Importer Footguns

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `FOOT-001` | blocker | merge-validator | `slug` controls update behavior; different slug means different incident path |
| `FOOT-002` | repairable | rule-validator | Empty `fund` objects are unsafe because they can still create `exploited_fund` |
| `FOOT-003` | warning | rule-validator | Lookup-backed fields can drift if canonical reuse is ignored |
| `FOOT-004` | warning | rule-validator | `logoImage` updates are sticky; re-import may retain the previous image for an existing slug |

## Repair Boundaries

The orchestrator may repair only these classes without inventing facts:

- deterministic formatting normalization
- empty-string to `null`
- array/object deduplication
- canonical spelling reuse where incident meaning is unchanged
- audit and fund cleanup
- evidence-backed field recovery from:
  - local materials
  - one focused researcher follow-up
  - web recovery from stronger sources

The orchestrator must not invent required-field facts. If a row still violates `REQ-*` after deterministic repair, local recovery, one user follow-up, and web recovery, the run still completes but that row must stay out of the synced JSON and move to the exception lane.
