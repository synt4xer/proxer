# Proxer Plan

Living plan. Last updated 2026-11-22, revalidated against the codebase after shipping P0, P1, and P2. `CHANGELOG.md` (kept as a local file, not published) records what each tag contains; this file tracks what has already shipped (with measured impact) and what comes next by priority.

## North star

Proxer exists to cut the number of bytes an agent spends to find and change code. A raw scan pays for every matching line; Proxer answers from a SQLite FTS5 index with a bounded, chunked payload the agent can pass straight to `read` and `edit`. Speed supports that goal but is not the goal: a payload that is small and wrong costs more than a payload that is large and right.

Every item below is judged by that goal: does it lower the context cost, or make the reduced payload more reliable?

## How to read this plan

- **Shipped Work & Measured Impact** records completed milestones (`P0`–`P2`), the exact measurements that validated them, and the costs accepted along the way. `[x]` means shipped and verified.
- **Upcoming Work (By Priority)** orders open work (`Priority 1`–`Priority 4`) by expected impact-to-effort ratio. Each tier has **Why** (codebase/benchmark evidence), **Tasks** (`[ ]`), and **Done when**.
- History before this plan — v0.2.0 (first working CLI) and v0.3.0 (project config and stale-read guard) — lives in `CHANGELOG.md`.

---

## Shipped Work & Measured Impact

| Milestone | Status | Problem Solved | Measured Impact |
| --- | --- | --- | --- |
| **P0 — Freshness** | Shipped (`v0.4.0`) | Silent stale index after external edits | Delta refresh on read path: **0.7 ms/changed file** at 32k chunks; delete by `rowid` cut per-file delete from **10.6 ms → 0.05 ms**; rare-term `search` + refresh is **38.6 ms** at 8,000 files (`--no-refresh`: **8.9 ms**). |
| **P1 — Search precision & cost** | Shipped (`v0.5.0`) | Invisible path tokens, noisy docs/minified top-5 hits, silent truncation | `bm25(chunk_search, 0, 5, 0, 0, 1)` over 40× window (max 200): **24.8 ms** vs **28.1 ms** at 16k chunks; **52.9 ms** vs **74.1 ms** at 65.8k chunks. Path-only tokens (`000001`, `init`, `mysql`, `up`) went **0 → 1** row; Go-side dedupe + `Code → Docs → Assets` demotion + `total`/`truncated`/`--kind`/`-n`. |
| **P2 — Long-line handling** | Shipped (post-`v0.5.0`) | `read` crashed on >64 KB lines; minified bundles bloated index & payloads | Replaced `bufio.Scanner` (64 KB ceiling) with unbounded line reader + explicit line truncation while preserving full-file SHA-256 hash; skips files with lines >4,096 chars at index time (`schema_version` bump). |

### Baseline: 2026-11-16 Field Report & Pre-P0 Benchmark

Proxer was installed in a second project (232 indexable files, 214 chunks) and an agent skipped it for a whole task, using `grep` and `sed` instead. The report was a cost comparison, not a preference:

| query | proxer payload | `grep` payload | winner |
| --- | --- | --- | --- |
| `Rebind` | 1,203 B | 3,715 B / 35 hits | proxer |
| `sqlRepo` | 1,186 B | 2,387 B / 22 hits | proxer |
| `normalizeDriver` | 724 B | 381 B / 4 hits | `grep` |

Proxer wins only when the hit count is large. Once the target is a handful of known lines, `grep -n` is smaller and always current. Three defects widened that gap:

1. **A stale index is silent.** With no database, or with files written after the last `index`, `search` returned `[]` and exit 0. → Fixed in **P0**.
2. **Search only matches `content`.** `db/migrations/mysql/000001_init.up.sql` could not be found by `000001`, `init`, `mysql`, or `up`. → Fixed in **P1**.
3. **Ranking spends the five slots badly.** Markdown (`PLAN.md`), minified JS (`static/htmx.min.js`), and duplicate chunks from the same file crowded out code hits, with no truncation indicator. → Fixed in **P1**.
4. **Long lines broke `read` and bloated payloads.** `read` exited 1 (`bufio.Scanner: token too long`) past 64 KB, and `static/htmx.min.js` (50,917 chars on one line) returned 52,329 bytes in a single payload. → Fixed in **P2**.

Pre-P0 baseline on synthetic Go tree (8,000 files / 63 MB / 16,008 chunks, same machine):

| operation | cost |
| --- | --- |
| stat-only walk (no reads) | 40 ms |
| full `index` (walk + read + chunk + insert) | 1.34 s |
| `search` for a rare term | 10 ms |
| `search` for `func` (matches most chunks) | 120 ms |

On the real field-report project (784 files on disk, 214 chunks): stat-only walk 17–49 ms, `search` 10 ms.

### P0. Freshness: delta index, refreshed on the read path (`v0.4.0`)

#### What changed

Replaced full-rebuild snapshots with a manifest-tracked delta refresh on the read path, backed by a `rowid` mapping table so deletes no longer scan the FTS5 table:

| delete strategy | per changed file | 50 changed files |
| --- | --- | --- |
| `WHERE filepath = ?` (before P0) | 10.6 ms | 531 ms |
| delete by `rowid` via a mapping table | 0.05 ms | 2.5 ms |

- [x] Add `index_chunks(rowid, filepath)` with an index on `filepath`, populate it wherever chunks are inserted, and delete through it.
- [x] Add `indexed_files(filepath, size, mtime_ns, hash)` and `meta(key, value)` carrying `schema_version`, `chunk_size`, `overlap_size`, `indexed_at`.
- [x] Turn `index` into a delta: walk, compare `size` and `mtime_ns` from `DirEntry.Info()`, read/hash only when they differ, re-chunk only on a hash change, and drop removed paths in one transaction.
- [x] Refresh before `search` queries (lock-free when nothing changed; single write transaction when diff is non-empty).
- [x] `ReindexFile`, used by `edit`, writes the manifest row in the same transaction as the chunks.
- [x] Rebuild legacy databases missing a manifest once, clearing `indexed_at` and stamping `schema_version`.
- [x] Return `{root, indexed_at, refreshed, results}` from `search`; build a missing database instead of returning `[]`.
- [x] Add `--no-refresh` on the CLI and `PROXER_NO_REFRESH=1` for MCP, omitting `refreshed` when read-only.
- [x] Separate SQLite store (`database/`) from walk/diff/chunking policy (`internal/indexer/`).
- [x] Add transition tests (new file, deleted file, touched-identical, content changed, missing manifest, no-op refresh) and update `README.md`, `docs/INTEGRATION.md`, and `skills/proxer/SKILL.md`.
- [x] Release as `v0.4.0`.

#### Measured impact

Measured 2026-11-16 on regenerated fixtures (2,000 files / 17 MB and 8,000 files / 68 MB, 32,000 chunks, median of 7 runs, process start included):

| workload | 2,000 files | 8,000 files |
| --- | --- | --- |
| `search` rare token, refreshed | 16.8 ms | 38.6 ms |
| `search` rare token, `--no-refresh` | 8.7 ms | 8.9 ms |
| `search` `func`, refreshed | 27.8 ms | 81.9 ms |
| `search` `func`, `--no-refresh` | 20.8 ms | 55.2 ms |
| `index`, first build | 433 ms | 2.00 s |
| `index`, nothing changed | 16.6 ms | 35.6 ms |
| `index`, 1,000 files changed | 544 ms | 717 ms |
| `edit` (reindex the edited file) | 15 ms | 15 ms |

- Delta cost: **0.7 ms/changed file** at 32,000 chunks (under the 2 ms target, down from 7.6 ms/file).
- **Kill criterion not triggered:** refresh + rare-term query stayed at **38.6 ms** (threshold was 200 ms).
- **Accepted costs:** first build rose from 1.34 s to 2.00 s (hashing + rowid map); freshness is gated on `size + mtime_ns` (same-tick, same-size edits wait for the next change); concurrent refreshes rely on last-writer-wins.

### P1. Search precision and cost (`v0.5.0`)

#### What changed

- [x] Replace `WHERE content MATCH ? ORDER BY rank` with `WHERE chunk_search MATCH ?` and `ORDER BY bm25(chunk_search, 0, 5, 0, 0, 1)` over a 40× candidate window (min 50, max 200), reporting `match` (`path`, `content`, or `both`) in Go.
- [x] Add `-n/--limit` (CLI) and `limit` (MCP), defaulting to 5 (clamped 1..50), plus `total` and `truncated` in the response.
- [x] Dedupe by file in Go before applying the cap (best-ranked chunk per file).
- [x] Demote noise in Go (`Code` → `Docs` → `Assets`, handling `.md`, `.mdx`, `.txt`, `.rst`, `*.min.*`, `*.map`, lockfiles) and add `--kind code|docs|all`.
- [x] Re-measure the `README.md` benchmark table on the same fixtures.

#### Measured impact

Measured directly against `.proxer.db` with SQLite 3.54.1 (matching bundled `modernc.org/sqlite` v1.62.0, median of 7 runs):

- **Faster & better ranking:** `bm25(chunk_search, 0, 5, 0, 0, 1)` over `LIMIT 50` ran in **24.8 ms** vs **28.1 ms** at 16,000 chunks, and **52.9 ms** vs **74.1 ms** at 65,800 chunks; `LIMIT 200` ran in **55.0 ms** at 65,800 chunks.
- **Path recall at zero cost:** `WHERE chunk_search MATCH ?` ran in **36 ms** vs **38 ms** at 16,000 chunks and turned path-only tokens (`000001`, `init`, `mysql`, `up`) from **0 → 1** row.
- **Go-side dedupe/demotion beat SQL alternatives:** SQL `row_number() over (partition by filepath)` added **+130 ms** at 65,800 chunks; rowid-bounded subqueries took **4.7 s**; `NOT filepath:"md"` took **131 ms** vs **53 ms**; two-query `kind` splitting took **76 ms** vs **54 ms**.
- **End-to-end (16,000 chunks):** `search -k func --no-refresh` **70 ms**, with refresh **110 ms**.
- **Rejected by measurement:** FTS5 `prefix='2 3 4'` (doubles DB size, no gain on common terms).

### P2. `read` handles long lines (post-`v0.5.0`)

#### What changed

- [x] Replace `bufio.Scanner` (64 KB ceiling) with a line-preserving reader that has no per-line size limit.
- [x] Truncate lines past a fixed width with an explicit marker so agents know content was cut.
- [x] Compute `hash` over the full file bytes so truncated reads still guard `edit`.
- [x] Skip files containing lines >4,096 characters at index time, bumping `schema_version` and reporting skipped file counts in `index`.
- [x] Add tests covering minified files, 100 KB single-line `read` (exit 0, bounded payload), and skipped long-line files in `index`.

#### Measured impact

- Eliminated `bufio.Scanner: token too long` (exit 1) when reading large single-line/minified files.
- Bounded `read` payloads (preventing 52 KB single-line blobs like `static/htmx.min.js`) and excluded generated long-line bundles from the FTS5 candidate pool entirely.

---

## Upcoming Work (By Priority)

| Priority | Theme | Expected Impact | Effort |
| --- | --- | --- | --- |
| **Priority 1** | Performance & Correctness Quick Wins | Cuts ~50% of FTS5 query work per search, removes wasted `stat()`/hash/JSON work, fixes `OR` `matchPart` bug | Low |
| **Priority 2** | Search Quality & Ranking Algorithms | Fixes `camelCase`/`snake_case` misses, replaces hard `fileKind` buckets, prevents multi-term `OR` noise flooding | Medium |
| **Priority 3** | Medium-Term Scale & Context Optimizations | Cuts indexing allocations, snaps chunks to natural block boundaries, avoids burst MCP re-walks | Medium |
| **Priority 4** | Adoption Surface | Teaches agents when to pick `proxer` vs `grep`, cleans `git status`, verifies real MCP harness loop | Low |

### Priority 1: Performance & Correctness Quick Wins

#### Why

Code inspection after P2 identified low-risk bottlenecks where Proxer repeats work on every `search`, `index`, `edit`, or MCP call without improving result quality:

- `PerformSearch` (`internal/tools/search.go`) executes two separate FTS5 `MATCH` queries per search (`matchTotal` via `SELECT count(*)` and `rankedCandidates` via `ORDER BY bm25(...) LIMIT ?`), and repeats both in the docs-excluded fallback path.
- `computeDiff` (`internal/indexer/diff.go`) calls `entry.Info()` (`stat`) before filtering out ignored directories or non-indexable files, while `internal/filesystem/scanner.go` scans slices linearly via `contains()` on every visited path. In addition, `".tailwind.css"` in `config.AllowedExtensions` never matches `filepath.Ext`.
- Both `computeDiff` → `storeCurrentFile` (`internal/indexer/diff.go`) and `PerformEdit` → `indexer.ReindexFile` (`internal/tools/edit.go`, `internal/indexer/engine.go`) read and SHA-256 hash the same file twice.
- MCP tool handlers (`internal/mcpserver/tools.go`) call `tools.Perform*`, which marshals structs to JSON, and then immediately `json.Unmarshal` the JSON string back into structs.
- `matchPart` (`internal/tools/search.go`) returns `"both"` whenever *any* term matches `filepath`, even in multi-term `OR` queries where one term only matched the path and not the content.

#### Tasks

- [x] **Single-query search + total count (`internal/tools/search.go`):** Combine `matchTotal` and `rankedCandidates` into one FTS5 query using `count(*) OVER() AS total`.
  - *Trade-off:* Removes one full FTS5 inverted-index scan per query (and per fallback) with zero ranking change; verify window-function + `LIMIT` behavior in `modernc.org/sqlite`.
- [x] **Defer `stat()` and replace slice scans with map lookups (`internal/indexer/diff.go`, `internal/filesystem/scanner.go`, `internal/config/config.go`):**
  - Move `entry.Info()` in `computeDiff` until after `IsIndexableDir` / `IsIndexableFile` checks so ignored directories (`node_modules`, `.git`, etc.) and skipped extensions incur zero `stat` syscalls.
  - Replace `contains()` linear slice scans in `internal/filesystem/scanner.go` with prebuilt `map[string]struct{}` lookups.
  - Remove or handle `".tailwind.css"` explicitly, since `filepath.Ext` only returns `".css"`.
- [x] **Eliminate double file read and double SHA-256 hashing (`internal/indexer/diff.go`, `internal/tools/edit.go`, `internal/indexer/engine.go`):**
  - Pass the already-read bytes, hash, and prepared content from `computeDiff` into `storeCurrentFile` for changed files.
  - Pass the already-written content/bytes from `PerformEdit` directly into `indexer.ReindexFile` (or an internal helper) instead of re-reading and re-hashing from disk.
- [x] **Remove double JSON marshal/unmarshal in MCP handlers (`internal/mcpserver/tools.go`, `internal/tools/*.go`, `cmd/proxer/main.go`):**
  - Return typed structs from core `Perform*` functions and keep JSON encoding only at the CLI output boundary (`cmd/proxer/main.go`).
- [x] **Fix `matchPart` accuracy for multi-term `OR` queries (`internal/tools/search.go`):**
  - Distinguish `"path"`, `"content"`, and `"both"` accurately when only a subset of `OR` terms match `filepath`.

#### Done when

`search` runs one FTS5 query per pass instead of two, ignored trees incur no `stat` calls, changed/edited files are read and hashed once, MCP handlers avoid JSON round-trips, `matchPart` never mislabels path-only hits as `"both"`, and all existing unit/integration tests pass with zero regression.

### Priority 2: Search Quality & Ranking Algorithms

#### Why

The current search pipeline gives strong baseline recall, but three algorithmic gaps reduce top-5 precision for AI coding agents:

1. **CamelCase / snake_case blind spot:** FTS5 `unicode61` does not split `AuthMiddleware`, `sqlRepo`, or `HTTPServer` on case boundaries, so `middleware` misses `AuthMiddleware` and `repo` misses `sqlRepo`. Prefix matching only covers the start of an identifier. Earlier measurement on 16,000 chunks showed adding a `tails` column grows the database ~2% (75.8 MB → 77.6 MB) while raising insert time 8.5× (0.53 s → 4.48 s), so build cost must be bounded.
2. **Hard `fileKind` bucketing (`internal/tools/kind.go`):** `orderByKind` strictly places every `Code` hit before every `Test` hit before every `Docs` hit before every `Assets` hit. A weak 1-token mention in a code file always outranks an exact multi-token or filename match in a test or doc file.
3. **Unconditional `OR` query joining (`internal/tools/search.go`):** `buildMatchQuery` joins all terms with `OR`. Multi-term queries (e.g., `auth middleware token`) can flood the 200-candidate window with single-term hits on generic tokens (`token`, `get`, `err`).

#### Tasks

- [x] **Identifier sub-tokenization (`camelCase` / `snake_case`):**
  - Write a pure splitter with a test table (`AuthMiddleware` → `auth middleware`; `HTTPServer` → `http server`; `sqlRepo` → `repo`; `v2` unchanged), deriving only the tails since prefix matching already covers the head.
  - Add an auxiliary indexed FTS5 column (`tails`, preserving raw `content` for `snippet()` and `read`) and measure the first build at 32,000 chunks against P0's 2.00 s baseline: measured 2.16 s without `tails`, 4.39 s naive, 3.61 s after zero-alloc ASCII fast path on a worst-case 100%-camelCase fixture; narrowed to code files and tails >= 3 chars.
  - Land with a `schema_version` bump (`"1"` → `"2"`) so existing indexes rebuild cleanly once.
- [x] **Soft BM25 score multipliers (`internal/tools/kind.go`, `internal/tools/search.go`):**
  - Replace strict `fileKind` hard bucketing in `orderByKind` with soft multipliers applied in Go to negative SQLite `bm25()` scores (Code `1.0x`, Test `0.6x`, Docs `0.35x`, Assets `0.1x`) when `--kind all` is used, while keeping explicit `--kind code|docs` filtering intact.
  - *Trade-off:* Strong exact matches in tests or docs can outrank weak 1-token noise in code without losing code-first preference on equal matches.
- [x] **`AND`-first query coordination with `OR` fallback (`internal/tools/search.go`):**
  - For multi-term queries, execute an `AND` query first; if it returns fewer unique files than `limit`, fall back to `OR` and merge `AND` hits first.
  - *Trade-off:* Eliminates single-token noise flooding on multi-word queries while preserving recall, paying a second query only when `AND` is too strict.

#### Done when

`-k middleware` finds `AuthMiddleware`, multi-term queries rank co-occurring matches first without losing single-term fallback, strong test/doc matches can beat weak code noise while code remains preferred on ties, and 32,000-chunk build time stays within the 3 s budget.

### Priority 3: Medium-Term Scale & Context Optimizations

#### Why

As repositories grow or MCP sessions run many queries back-to-back, three structural improvements reduce allocations, improve chunk coherence for AI context windows, and cut redundant filesystem walks — all without breaking `CGO_ENABLED=0` or introducing a background daemon:

- `PrepareContentForIndex` (`internal/indexer/lines.go`) splits lines and joins them with `"\n"`, only for `Writer.write` (`database/chunks.go`) to immediately `strings.Split(content, "\n")` again.
- Fixed 60-line slicing (`ChunkSize = 60`, `OverlapSize = 15` in `internal/config/config.go`) can cut functions or blocks mid-body even when a blank line or top-level boundary is a few lines away.
- Long-lived MCP servers re-walk the entire tree on every consecutive `search` call, even milliseconds apart, and `dedupeByFile` (`internal/tools/search.go`) caps every file to 1 chunk even when a large file contains multiple distinct relevant sections.

#### Tasks

- [x] **Avoid `Split` → `Join` → `Split` allocations (`internal/indexer/lines.go`, `database/chunks.go`):**
  - `PrepareContentForIndex` now returns `[]string` directly and `Writer.StoreLines` / `writeLines` chunks the pre-split slice without re-joining and re-splitting.
- [x] **Heuristic line-boundary snapping in chunker (`database/chunks.go`):**
  - `snapChunkEnd` snaps chunk ends within a ±8-line window around `ChunkSize` (for windows >= 20 lines) to blank-line boundaries first, then zero-indent top-level lines, keeping `Overlap` continuous from the snapped end.
  - *Trade-off:* Better self-contained context for agents with minimal complexity and pure-Go portability preserved.
- [x] **MCP walk coalescing & multi-chunk per-file option (`internal/mcpserver/`, `internal/tools/search.go`):**
  - Added a 2 s in-memory `refreshCooldown` on `mcpserver.Server` so rapid sequential `search` calls skip redundant directory walks while `edit` immediately invalidates the cooldown.
  - Added `MaxPerFile` to `tools.SearchOptions` and `max_per_file` (default 1, clamped 1..5) to MCP `searchInput`, allowing `dedupeByFile` to return up to K non-overlapping chunks per file without changing the default 1-hit-per-file behavior.

#### Done when

Indexing avoids redundant line-slice round-trips, chunks align more cleanly to natural code boundaries, and rapid MCP search bursts avoid redundant walks while keeping freshness intact.

### Priority 4: Adoption Surface

#### Why

Cheap, and the only item that changes behaviour without changing code. The field-report project had the skill installed and the server never registered, so the tool list the skill advertises did not exist — an agent cannot adopt what is not there. And without a decision rule, an agent defends its `grep` habit, which on the measured evidence is a reasonable thing to do for small targets.

#### Tasks

- [ ] `skills/proxer/SKILL.md` gains a decision rule with the measured numbers: reach for `search` when the file is unknown or the hit count is large; go straight to `grep` or `read` when the path is already known.
- [ ] `make install` appends `.proxer.db*` to the target project's `.gitignore`, or prints the line when the file is absent. In the field-report project the database and its `-shm`/`-wal` files showed up as untracked noise in every `git status`.
- [ ] Verify the MCP server in a real harness and record the result with a date: `make install DEST=<project>`, register `proxer mcp`, confirm the `mcp__proxer__*` tools, run a `read` then `edit` round-trip.

#### Done when

An agent that read `SKILL.md` picks the cheaper tool per query and can say why, and the harness check is ticked off.

---

## Backlog (unordered)

Not scheduled. Order is decided when one of them becomes the reason a task failed.

- [ ] `status` and `doctor`. The manifest makes this nearly free: file and chunk counts, `indexed_at`, the effective skip list, and the files the last walk found changed. Answers "why is this file missing" without spending a search on it.
- [ ] `proxer watch` on top of the delta refresh. Still opt-in, still not a default.
- [ ] `.gitignore` parsing for the skip list, layered over `DefaultIgnoredDirs` and `.proxer.json`. This also removes the last reason `static/htmx.min.js` and its `-shm` neighbours reach the index.
- [ ] `.proxer.json` gains `extensions`, `max_file_size`, and `chunk_size`/`overlap_size`. The chunk size is carried in `meta` after P0, which is the prerequisite this item was waiting on.
- [ ] `edit` for create and delete, not only range replacement.
- [ ] Structured search filters: path prefix, extension, exact match. P1's path match covers the common case; this is the precise version.
- [ ] Deeper MCP surface: resources for the root and the config, prompts for the skill's order of operations.
- [ ] HTTP or SSE MCP transport for clients that cannot spawn a stdio process.

## Exploratory

Open design questions, not tasks.

- Single-writer bottleneck. A refresh on the read path sharpens this: every search may want a write transaction. Consider skipping the refresh when another process holds the write lock and the index is younger than the tree, rather than queueing behind it.
- Incremental FTS5 ranking. P1 answers the 120 ms common-term case with a weighted `bm25()` over a wide window, so a fragment-level index is only worth revisiting if a larger fixture breaks the 200-row assumption — measure before reopening.
- Monorepo roots. Decide whether `--root` stays single or becomes a list, and how one database maps to several packages.
- Snippet centering. The result reports the chunk boundary, not the exact hit line. Rank the chunk, then point at the matching line inside it, which makes the follow-up `read` cheaper.
- Windows path handling. Keys already use `filepath.ToSlash` and `..` is rejected. Confirm real behavior on Windows before claiming support.

## Non-goals

Deliberate. Reopen the decision before adding any of them.

- Semantic or type-aware search. A language server (`gopls`, `tsserver`, `rust-analyzer`) owns that. Proxer locates text; the LSP reasons about the program.
- A background daemon or file watcher. Freshness comes from a delta refresh at the start of each `search`, not from a process watching the tree.
- Replacing harness tools. MCP mode is additive, and the harness keeps its own `read`, `edit`, `bash`, and `write`.
- Rewriting shared git history, including moving a published tag.
- Verification through CI. It stays local (`build`, `test`, `clean`, `install`), and the measured table is the verification that matters.
- `.proxer.json` keys beyond `ignored_dirs`. Extensions, the size limit, and the chunk size stay compiled in until the backlog item above asks for them.

## Definition of done

Every item lands the way the existing work did:

1. One atomic conventional commit per change, in English.
2. Each commit verified on its own tree: `gofmt -l` clean, `go vet`, `CGO_ENABLED=0 go build ./...`, `go test ./...`.
3. Tests for the new behavior, including the error paths. For P0 that was one test per transition: new file, deleted file, touched but identical, content changed, manifest missing, and a refresh that changes nothing.
4. A number, not an adjective. Anything claimed as faster or smaller in a commit message or the README is measured against the baseline table in this file or the one in `README.md`, on the same machine and the same fixture, and the measurement is recorded.
5. Docs updated: `README.md` for user-facing behavior, `skills/proxer/SKILL.md` for the CLI contract, `docs/INTEGRATION.md` for harness setup, and `CHANGELOG.md` for the release record.
6. Released per the versioning rule: a feature is a MINOR bump, a fix is a PATCH, and a docs-only change gets no tag.

## Open questions

- ~~Does the 40 ms walk at 8,000 files plus a 10 ms query hold up as acceptable?~~ Answered by measurement: refresh plus a rare query is 38.6 ms at 8,000 files, so the refresh stays on the read path and the loud `stale` fallback was not needed.
- ~~Should the hash be computed on every delta build, or only when size or `mtime_ns` disagrees?~~ Answered: only when the gate opens. The same-tick same-size edit remains the accepted blind spot and is documented in README and SKILL.md.
- ~~Is bounding the ranked candidate set enough to fix the 120 ms common-term search, or does ranking have to move to a fragment-level index?~~ Answered 2026-11-17: neither is needed. `bm25()` with a path weight over a 200-row window answers in 55 ms at 65,800 chunks, faster than today's `ORDER BY rank LIMIT 5` at 74 ms. Both the bounded-subquery and the SQL-dedupe variants are measured dead (4.7 s and +130 ms).
- ~~What is the minified-file test: one line past a width, a mean line length, or an extension rule for `*.min.*`?~~ Answered: path rules in P1 (`*.min.*`, `*.map`, lockfiles — free, and they cover the file the field report hit), plus a max-line-width skip at index time in P2, because only the indexer sees the body.
- Should multi-term queries stay `OR`? Scheduled in **Priority 2** as `AND`-first with an `OR` fallback: try `AND` for multi-term precision and fall back to `OR` when results are empty/insufficient, preserving recall while avoiding single-token noise.
- Does the `match: path|content` field earn its place in the payload, or does the caller only ever need the result? Ask after P1 ships.
- Do we claim Windows support, and therefore add a Windows CI runner — which collides with the no-CI non-goal?
