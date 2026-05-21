# pre-lumos

Turn blockchain incident materials into Lumos importer-ready JSON and sync them into `seed/import_{YEAR}.json`.

## What It Is For

Use `pre-lumos` when a security researcher has:

- local notes
- raw JSON or CSV
- postmortems
- audit links
- mixed URLs and inline summaries

and wants a single importer-ready JSON array that can go straight into the Lumos ingestion path defined by this skill.

## What It Produces

- final visible output: one fenced `json` block
- file output: `seed/import_{YEAR}.json`
- sync behavior:
  - create `seed/` if missing
  - merge by `slug`
  - replace matching rows
  - append new rows

## Internal Flow

`pre-lumos` runs this pipeline:

1. intake and source collection
2. local evidence extraction
3. ask-once gap recovery
4. normalization to the Lumos importer contract
5. row validation swarm
6. repair and revalidation
7. merge validation before write
8. sync to `seed/import_{YEAR}.json`

Validation is always on. The validator team is read-only:

- `contract-validator`
- `rule-validator`
- `merge-validator`

Only the main workflow may repair rows or write files.

## Recommended Prompt Shape

For the cleanest runs, include:

- one or more local file paths
- the target year if known
- whether this is a new incident or a revision
- any preferred slug if the incident identity is already known

Template:

```text
Use pre-lumos on [path1] [path2] [...] for [YEAR].
This is [a new incident / a revision].
Preferred slug: [slug-if-known].
Sync into seed/import_[YEAR].json.
```

## Recovery Behavior

If required data is missing, `pre-lumos` will:

1. re-check local materials
2. ask the researcher once
3. use stronger web sources when available
4. repair deterministic formatting issues
5. keep only importer-safe rows in the synced JSON

Rows that still cannot be made importer-safe stay out of the synced file and appear as exceptions.

## Not For

- legacy CSV ingestion paths
- legacy year-specific ingestion paths
- free-form incident writeups without JSON output
- Prisma schema or migration work

## Files

- [SKILL.md](SKILL.md)
- [run-pre-lumos-json-pipeline.md](workflows/run-pre-lumos-json-pipeline.md)
- [pre-lumos-normalization-rules.md](references/pre-lumos-normalization-rules.md)
- [pre-lumos-output-contract.md](references/pre-lumos-output-contract.md)
- [pre-lumos-source-materials.md](references/pre-lumos-source-materials.md)
