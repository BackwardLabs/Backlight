---
name: pre-lumos
description: >-
  Use when a blockchain security researcher wants to turn exploit notes,
  local evidence files, raw datasets, report drafts, or mixed incident
  materials into Lumos importer-ready incident JSON, run a rule-based final
  validation swarm with repair and revalidation, and sync the result into
  seed/import_{YEAR}.json.
allowed-tools:
  - Read
  - Glob
  - Grep
  - Write
  - Edit
  - AskUserQuestion
  - Task
  - TaskCreate
  - TaskList
  - TaskUpdate
  - TodoRead
  - TodoWrite
  - WebFetch
  - WebSearch
---

# Pre-Lumos

## Essential Principles

1. **Importer compatibility beats prose quality.**
   Every output row must be shaped for the Lumos JSON importer contract defined by this skill's local references, not for human-readable reporting. If a value looks good in prose but violates importer behavior, fix it.

2. **Slug is the identity key.**
   Treat `slug` as the update key for sync and DB ingestion. Never change an existing slug casually because that creates a new incident path.

3. **Prefer canonical reuse over vocabulary expansion.**
   For lookup-backed fields, reuse existing canonical values whenever possible. New spellings create DB drift and UI inconsistency.

4. **Never stop at missing data.**
   Ask the researcher for missing material once when it is high-value. If they cannot answer, continue with available evidence, use web research when possible, and emit the best valid JSON you can without inventing facts.

5. **Final validation is always on.**
   Every run must pass through a validation swarm, a repair pass, and a final revalidation gate before sync. Validation findings stay internal; only importer-safe JSON survives to the synced file.

6. **The orchestrator is the only writer.**
   Validator agents are read-only. They may classify, cite rules, and recommend fixes, but only the main workflow may repair rows, merge by `slug`, or write `seed/import_{YEAR}.json`.

## When to Use

- When converting blockchain exploit research notes into importer-ready incident JSON
- When normalizing raw incident material from local files into the Lumos importer contract defined by this skill
- When reviewing CSV rows, markdown notes, postmortems, or mixed-source evidence for DB ingestion
- When merging new incidents into an existing `seed/import_{YEAR}.json` file
- When a researcher provides file paths and wants the agent to extract, normalize, and sync DB-ready incidents
- When unresolved gaps should be asked once, then completed through web research or safe `null` fallback without stopping output generation

## When NOT to Use

- Do not use for legacy CSV ingestion workflows; use the CSV-specific process instead
- Do not use for legacy year-specific ingestion paths; use the matching process instead
- Do not use for free-form report writing that does not need DB-ready JSON output
- Do not use for schema migration or Prisma model changes; use the DB operations workflow instead

## Pattern

Workflow:
- [run-pre-lumos-json-pipeline.md](workflows/run-pre-lumos-json-pipeline.md)

Validation team:
- [contract-validator.md](agents/contract-validator.md)
- [rule-validator.md](agents/rule-validator.md)
- [merge-validator.md](agents/merge-validator.md)

## Quick Reference

### Core References

| File | Purpose |
|------|---------|
| [pre-lumos-normalization-rules.md](references/pre-lumos-normalization-rules.md) | Importer rules, field constraints, and footguns |
| [pre-lumos-source-materials.md](references/pre-lumos-source-materials.md) | Supported input materials and missing-data handling |
| [pre-lumos-output-contract.md](references/pre-lumos-output-contract.md) | Output JSON contract and file sync semantics |
| [contract-validator.md](agents/contract-validator.md) | Row-shape and required-field validator |
| [rule-validator.md](agents/rule-validator.md) | Importer-semantic and normalization validator |
| [merge-validator.md](agents/merge-validator.md) | Merged-payload validator before write |

### Output Target

- `seed/import_{YEAR}.json`

Sync rule:
- If file exists, merge by `slug`
- If file does not exist, create it
- If `seed/` directory does not exist, create it, then write the file

### Prompt Inputs

The researcher may pass:
- one or more local file paths
- inline notes
- URLs
- year hints
- target incident names or slugs

Prefer local file paths first.

## Reference Index

| File | Content |
|------|---------|
| [references/pre-lumos-normalization-rules.md](references/pre-lumos-normalization-rules.md) | Canonical normalization rules and importer caveats |
| [references/pre-lumos-source-materials.md](references/pre-lumos-source-materials.md) | Intake model for local files, notes, and web evidence |
| [references/pre-lumos-output-contract.md](references/pre-lumos-output-contract.md) | Final JSON shape and sync contract |
| [agents/contract-validator.md](agents/contract-validator.md) | Read-only contract validator for candidate rows |
| [agents/rule-validator.md](agents/rule-validator.md) | Read-only rule validator for importer semantics |
| [agents/merge-validator.md](agents/merge-validator.md) | Read-only final gate for merged payloads |
| [workflows/run-pre-lumos-json-pipeline.md](workflows/run-pre-lumos-json-pipeline.md) | End-to-end execution workflow |

## Success Criteria

- [ ] Input materials were enumerated and read or explicitly marked unusable
- [ ] Missing high-value facts were asked once when appropriate
- [ ] Remaining unresolved gaps were handled with browsing, safe `null`, or exception handling
- [ ] Every row is valid for the Lumos JSON importer contract defined by this skill
- [ ] Row validation swarm ran on normalized candidate rows
- [ ] Repair work was applied only by the orchestrator
- [ ] Merge validation ran on the final merged payload before write
- [ ] Lookup-backed fields reuse canonical values where possible
- [ ] Final visible output is a single fenced JSON array by default
- [ ] Final output file is synced to `seed/import_{YEAR}.json`
- [ ] Existing file contents were merged by `slug`, not blindly duplicated
- [ ] The resulting file is immediately suitable for DB ingestion
