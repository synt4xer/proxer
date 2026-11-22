# Benchmarks & Performance

Proxer is designed around a single constraint: **an agent's context window is the scarce resource**. Raw text search (`grep` / `rg`) dumps every matching line into the prompt, whereas Proxer keeps payloads bounded to ~1 KB regardless of repository size.

## Context Payload & Scale (2,000 vs 8,000 Files)

| Workload | 2,000 files (17 MB) | 8,000 files (68 MB) |
| --- | --- | --- |
| `search` payload for `func` | 1.0 KB | 1.0 KB |
| `rg` payload for `func` | 7.3 MB / 60k lines | 29.2 MB / 240k lines |
| `search` for a token that never appears, refreshed | 16.8 ms | 38.6 ms |
| `search` for the same token, `--no-refresh` | 8.7 ms | 8.9 ms |
| `search` for `func`, refreshed | 27.8 ms | 81.9 ms |
| `search` for `func`, `--no-refresh` | 20.8 ms | 55.2 ms |
| `index`, first build | 433 ms | 2.00 s |
| `index`, nothing changed | 16.6 ms | 35.6 ms |
| `index`, 1,000 files changed | 544 ms | 717 ms |
| `edit` (reindex the edited file) | 15 ms | 15 ms |

### Why It Scales

- **Bounded output:** `search` returns at most 5 distinct files by default (60-line chunks with 15-line overlap), expandable up to 50 via `-n/--limit`, with `total` and `truncated` signaling how much was omitted.
- **Delta `index`:** The first build reads and hashes every file and writes the chunk map. Subsequent runs with no changes perform a stat-only walk (~36 ms at 8,000 files) without opening a write transaction. Changed files cost ~0.7 ms each to re-read and re-chunk at 32,000 chunks.
- **Auto-refresh on `search`:** `search` refreshes the index before querying unless `--no-refresh` (or `PROXER_NO_REFRESH=1`) is set.
- **Single-file `edit` reindex:** `edit` reindexes only the modified file, keeping edit latency constant (~15 ms) regardless of repository size.

## Ranking & Matching Delta (`v0.4.0` vs `v0.5.0`)

Measured as a like-for-like comparison between `v0.4.0` and `v0.5.0` on the same generated Go trees:

| Workload | v0.4.0 | v0.5.0 |
| --- | --- | --- |
| `search func`, 2,000 files / 16,000 chunks, `--no-refresh` | 47.3 ms | 43.7 ms |
| `search func`, 8,000 files / 64,000 chunks, `--no-refresh` | 156.4 ms | 126.7 ms |
| `search func`, the same two fixtures, refreshed | 63.3 / 223.3 ms | 60.9 / 194.9 ms |
| `search func -n 20`, the same two fixtures, `--no-refresh` | not available | 42.3 / 127.5 ms |
| `search file7722`, a token that only names a path, `--no-refresh` | 0 hits | 1 hit |
| `search func` where 50 docs fill the ranked window | 47.5 ms, five `doc*.md` | 78.1 ms, code first |

`ORDER BY bm25(chunk_search, 0, 5, 0, 0, 1)` over a 200-row window is faster than `ORDER BY rank LIMIT 5`, and requesting 20 files costs roughly the same as 5 because ranking is dominated by the match rather than the window. When documentation fills the ranked window, one extra query recovers code chunks so a broad query on a docs-heavy tree returns code first instead of five markdown files.

## Methodology

- Hardware: Apple-silicon Mac with a warm cache over generated Go trees (2,000 files / 17 MB and 8,000 files / 68 MB, chunking into ~8,000 and ~32,000 rows in the primary table, and 16,000 / 64,000 chunks in the `v0.4.0` vs `v0.5.0` comparison).
- Timing: Median of seven `proxer` runs, process startup included. Changed-file runs write files immediately before the timed build.
- Baseline: `rg` is ripgrep `15.2.0` given an explicit path and default output.
