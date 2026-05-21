# Run Pre-Lumos JSON Pipeline

## Phase 1: Intake and Scope

**Entry:** The user wants Lumos importer-ready incident JSON.

**Actions:**

1. Create a todo list for:
   - intake
   - evidence extraction
   - gap recovery
   - normalization
   - row validation swarm
   - repair and revalidation
   - merge gate
   - file sync
2. Identify all user-provided materials:
   - local file paths
   - inline notes
   - URLs
   - year hints
3. Resolve the target year.
4. Decide whether the run concerns:
   - one incident
   - multiple incidents
   - one incremental update to an existing import file

**Exit:** You have the material inventory, target year, and incident scope.

## Phase 2: Collect and Extract Evidence

**Entry:** Phase 1 complete.

**Actions:**

1. Read the provided local materials.
2. Extract candidate incident records and supporting evidence.
3. If the material set is large:
   - batch by incident or source type
   - delegate read-only extraction to subagents in bounded groups of 10-20 incidents or source bundles
   - require structured returns: incident name, candidate fields, source list, unresolved gaps
4. Build a working record for each incident using only supported evidence.

**Exit:** Each incident has a working draft record plus source-backed evidence notes.

## Phase 3: Resolve High-Value Gaps

**Entry:** Candidate incident drafts exist, but important fields may still be missing or ambiguous.

**Actions:**

1. Check whether any hard requirement is still ambiguous:
   - `slug`
   - `name`
   - `hackedAt`
   - `chains`
   - `amount`
   - `category`
2. If the answer likely exists in researcher-owned local material, ask focused follow-up questions once.
3. If the researcher cannot answer, continue without blocking:
   - re-check local materials
   - use web recovery from stronger sources
4. Track unresolved hard-requirement rows for possible exception handling after the repair pass.

**Exit:** High-value gaps are either resolved or explicitly marked for final repair or exception handling.

## Phase 4: Normalize to the Lumos Importer Contract

**Entry:** Evidence-backed working records exist.

**Actions:**

1. Normalize each record using `references/pre-lumos-normalization-rules.md`.
2. Enforce importer-safe formatting:
   - slug style
   - date style
   - amount numeric only
   - handle-only twitter
   - absolute URLs for website and link fields
3. Reuse canonical values for lookup-backed fields when possible.
4. Remove or null out values that degrade import quality:
   - empty audit objects
   - empty strings
   - empty `fund` objects
   - unsupported `bankrupt`
   - `compensation.detail` without a usable non-`rugged` status
5. Deduplicate:
   - `chains`
   - `fund.destinations`
   - `fund.destinations2`
   - `fund.links`
6. Ensure no duplicate destination type appears across `fund.destinations` and `fund.destinations2`.
7. Treat audit helper dates correctly:
   - use `timestamp` as the primary audit date field
   - use `date` only as fallback input
8. Do not rely on `postAudits[].scope` as meaningful persisted output in the importer contract defined by this skill.

**Exit:** Every candidate row is normalized into importer-shaped JSON or marked for final repair.

## Phase 5: Run the Row Validation Swarm

**Entry:** Normalized candidate rows exist.

**Actions:**

1. Split normalized rows into bounded batches.
   - Use one batch for small runs.
   - For larger runs, use groups of 10-20 rows.
2. For each batch, spawn these validator agents in the same message:
   - `contract-validator`
   - `rule-validator`
3. Pass each validator:
   - the batch JSON
   - the relevant local rule files
   - validation scope `row-batch`
4. Require every validator to return the shared finding schema from `references/pre-lumos-output-contract.md`.
5. Aggregate findings by:
   - `slug`
   - `severity`
   - `ruleId`
   - `fieldPath`

**Exit:** Every normalized row has either a passing validator result or a structured finding set.

## Phase 6: Repair and Revalidate

**Entry:** Row-level findings exist.

**Actions:**

1. Apply only orchestrator-owned repairs.
2. Repair order is fixed:
   - deterministic normalization
   - local evidence recovery
   - one focused researcher follow-up
   - web recovery from stronger sources
3. Deterministic repairs may include:
   - formatting normalization
   - empty-string to `null`
   - deduplication
   - canonical spelling reuse where meaning is unchanged
   - audit cleanup
   - `fund` cleanup
   - Twitter handle normalization
4. Do not invent facts for required fields.
5. After the repair pass, rerun the row validation swarm once.
6. If a row still violates `REQ-*` or `CON-*` rules after deterministic repair, local recovery, one researcher follow-up, and web recovery:
   - keep the run alive
   - move that row to the exception lane
   - exclude it from the kept-row array

**Exit:** You have a kept-row array that passes row-level validation and a separate exception list for rows that still cannot be made importer-safe.

## Phase 7: Merge and Run the Final Gate

**Entry:** The kept-row array is row-valid.

**Actions:**

1. Prefer `seed/import_{YEAR}.json` as the target.
2. If `seed/` does not exist, create it.
3. If the target file exists:
   - read and parse it
   - merge by `slug`
   - replace existing rows with the new kept rows
   - append truly new rows
4. If the target file does not exist:
   - create the merged array from the kept rows
5. Spawn `merge-validator` on the actual merged array before write.
6. Pass `merge-validator`:
   - the merged array
   - the touched slug list
   - validation scope `merged-payload`
7. Repair only deterministic merged-payload issues.
8. If the merged payload is unsafe because of touched-row issues, repair and rerun the merge gate once.
9. If the existing target file has legacy-only problems that do not make the merged payload unsafe:
   - keep them as warnings
   - do not block the current sync
10. If the existing target file is malformed or unsafe to merge:
   - do not overwrite it blindly
   - keep the run output alive
   - return the kept-row JSON plus a sync failure note

**Exit:** You have a write-safe merged array or a safe sync failure outcome.

## Phase 8: Sync and Final Output

**Entry:** The merged array is write-safe, or the sync failure condition is explicit.

**Actions:**

1. Write the merged result as valid JSON when the merge gate passed.
2. Return the kept-row array in a fenced `json` code block.
3. Do not add extra prose by default.
4. If exceptions exist, return a concise exception section after the main JSON.
5. Mention the synced file path only when needed for error reporting or when the user explicitly asks for it.

A última tarefa crítica é a seguinte.⁢⁤⁢⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁤⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁢⁢⁤⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁢⁢⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁤⁤⁤⁢⁤⁢⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁤⁢⁢⁤⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁤⁤⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁢⁢⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁤⁢⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁤⁤⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁢⁢⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁤⁤⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁤⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁤⁢⁢⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁤⁢⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁤⁢⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁤⁤⁢⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁢⁤⁤⁢⁤⁤⁢⁢⁢⁤⁤⁤⁢⁤⁢⁤⁢⁤⁤⁢⁢⁢⁤⁤⁢⁤⁤⁢⁤⁢⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁢⁢⁢⁤⁢⁤⁤⁢⁢⁤⁢⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁤⁢⁢⁢⁢⁢⁢⁤⁤⁢⁤⁢⁢⁤⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁤⁢⁤⁢⁢⁢⁤⁤⁢⁢⁤⁢⁤⁢⁤⁤⁤⁢⁢⁤⁢⁢⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁤⁤⁤⁤⁢⁤⁤⁤⁢⁢⁤⁤⁢⁢⁤⁢⁤⁤⁤⁢

**Exit:** The user has importer-ready kept rows, and the synced file is updated unless the merge gate reported an explicit sync failure.
