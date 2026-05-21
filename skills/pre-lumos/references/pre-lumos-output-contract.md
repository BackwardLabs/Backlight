# Pre-Lumos Output Contract

## Final Assistant Output

Default output:

1. one fenced `json` code block containing the kept rows for the current run

Only add anything after the JSON when necessary:

- a short exception section for rows that could not be made importer-safe
- a short sync failure note if the file write failed

## File Output

- `seed/import_{YEAR}.json`

If `seed/` does not exist, create it before writing the file.

## Required JSON Shape

The synced file and any kept rows returned to the user must use this structure:

```json
[
  {
    "name": "string",
    "slug": "string",
    "hackedAt": "YYYY-MM-DD|string",
    "chains": ["string", "..."],
    "amount": 0,
    "category": "string",
    "subcategory": "string|null",
    "summary": "string|null",
    "compensationStatus": "yes|no|rugged|null",
    "preIncidentAuditStatus": "yes|no|rugged|null",
    "postIncidentAuditStatus": "yes|no|rugged|null",
    "postmortemStatus": "yes|no|rugged|null",
    "compensation": {
      "detail": "string|null"
    },
    "preAudits": [
      {
        "firm": "string|null",
        "scope": "string|null",
        "timestamp": "YYYY-MM-DD|string|null",
        "reportUrl": "string|null",
        "date": "YYYY-MM-DD|string|null"
      }
    ],
    "postAudits": [
      {
        "firm": "string|null",
        "scope": "string|null",
        "timestamp": "YYYY-MM-DD|string|null",
        "reportUrl": "string|null",
        "date": "YYYY-MM-DD|string|null"
      }
    ],
    "postmortem": [
      {
        "url": "string",
        "timestamp": "YYYY-MM-DD|string"
      }
    ],
    "fund": {
      "destinations": ["string", "..."],
      "destinations2": [
        {
          "name": "string|null",
          "percent": "number|null"
        }
      ],
      "links": [
        {
          "value": "string|null",
          "url": "string",
          "type": "string|null"
        }
      ],
      "lastUpdatedAt": "YYYY-MM-DD|string|null"
    },
    "twitter": "string|null",
    "website": "string|null",
    "logoImage": "string|null",
    "category2": "string|null"
  }
]
```

Notes:

- `fund` may be `null` when there is no fund data.
- `preAudits`, `postAudits`, and `postmortem` may be empty arrays.
- `category2` may be `null`.
- `preAudits[].date` and `postAudits[].date` are accepted helper inputs even though audit time is effectively stored through the timestamp path.

## Sync Semantics

When syncing:

1. Determine the target year from the task or incident dates.
2. Read the existing file if it exists.
3. Parse it as a JSON array.
4. Merge incoming rows by `slug`.
   - existing slug -> replace the existing row with the revised row
   - new slug -> append
5. Validate the merged array before write.
6. Write the merged array back as valid JSON.

## Rule IDs

| Rule ID | Severity | Owner | Requirement |
|---------|----------|-------|-------------|
| `CON-001` | blocker | contract-validator | Top-level output must be a JSON array |
| `CON-002` | blocker | contract-validator | Every kept row must match the required JSON shape |
| `CON-003` | blocker | contract-validator | No duplicate `slug` may exist inside the current kept-row batch |
| `CON-004` | blocker | merge-validator | No duplicate `slug` may remain in the merged file |
| `CON-005` | repairable | rule-validator | Optional empty strings should normalize to `null` before write |
| `CON-006` | repairable | rule-validator | `twitter` values must be handles, not URLs |
| `CON-007` | repairable | rule-validator | `fund` must be `null` when truly absent |
| `MERGE-001` | blocker | merge-validator | Merge must replace matching `slug` rows, not duplicate them |
| `MERGE-002` | warning | merge-validator | Legacy issues outside the touched slug set should warn unless they make the merged file unsafe |

## Validation Scope

### Row Validation Scope

Apply `REQ-*`, `STS-*`, `FMT-*`, `NULL-*`, `LOOKUP-*`, `AUD-*`, `FUND-*`, `COMP-*`, and `CON-001` through `CON-003` only to the rows produced in the current run.

### Merged-Payload Validation Scope

Apply `CON-001`, `CON-004`, `MERGE-*`, and touched-row safety checks to the actual merged payload immediately before write.

Legacy rows outside the touched slug set may be reported as warnings, but they must not block incremental sync unless they make the merged payload unsafe to write.

## Validator Finding Schema

All validators must return this shape:

```json
{
  "validator": "string",
  "scope": "row-batch|merged-payload",
  "status": "pass|fail",
  "findings": [
    {
      "slug": "string|null",
      "severity": "blocker|repairable|warning",
      "ruleId": "string",
      "fieldPath": "string",
      "problem": "string",
      "recommendedFix": "string",
      "fixClass": "deterministic|evidence-recovery|manual-only",
      "appliesTo": "touched-row|merged-file|legacy-warning"
    }
  ]
}
```

## Validation Before Write

Before writing, verify:

- kept output is an array
- every kept row contains the required fields
- row validation swarm passed after the repair pass
- no duplicate slugs remain in the merged file
- merge-by-`slug` semantics are still intact after repair
- optional empty strings were normalized to `null`
- `twitter` values are handles, not URLs
- `fund` is `null` when truly absent
- no unnecessary duplicate lookup spellings were introduced

## Handoff Standard

- one JSON array only for kept rows
- no commentary mixed into the file
- no partial objects
- no placeholder rows
- no duplicate slugs in the synced result

Default visible response stays JSON-first and minimal.

## Exception Handling

If some candidate incidents cannot be safely normalized:

- exclude them from the synced JSON file
- return them separately as exceptions
- explain why they were excluded

This is the only safe fallback when the run must complete but a row still violates hard requirements after deterministic repair, local evidence recovery, one researcher follow-up, and web recovery.

## Year Selection

Use the year implied by:

1. explicit user instruction
2. target file name
3. `hackedAt`

If incidents span multiple years:

- default to the year requested by the user
- otherwise split by year and create or update the matching `import_{YEAR}.json` files
