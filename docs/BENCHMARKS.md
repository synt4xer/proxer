# Benchmarks & Performance

Proxer is built around one constraint: **an agent's context window and retry turns are the scarce resources**.

Standard shell tools (`rg`, `grep`, `find`, `sed`, and ad-hoc `python3` scripts) optimize for raw terminal throughput, not LLM prompt budgets. They either dump megabytes of unranked lines into the context window or return `0 hits` on natural multi-word and `camelCase` queries—forcing the agent into multi-turn regex trial-and-error.

Every table below is labeled with its measurement boundary:
- **`[CLI end-to-end]`** — full `proxer` subprocess invocation (process startup, flag parsing, SQLite open, query/refresh, JSON stdout).
- **`[in-process modernc.org/sqlite SQL]`** — Go SQL call directly against an already-open DB (no process startup or walk).
- **`[MCP JSON-RPC]`** — stdio tool call over a running `proxer mcp` process (not yet benchmarked separately; shares the same in-process engine).

---

## 1. Search: `proxer search` vs `rg`, `grep`, `find`, and `python3`

Historical baseline (`v0.4.0`–`v0.5.0`, pre-`8b27a6a`) measured on synthetic mixed-content trees (**2,000 Go files / 17 MB** and **8,000 Go files / 68 MB**, plus 40 Markdown guides and a 144 KB single-line minified JS bundle). For current post-`P2.1` latency on the reproducible 72,000-chunk fixture—including common-term worst cases (`func`, `if`, `context value`)—see [§5](#5-reproducible-audit-fixture--query-cost-ceiling-p21--2026-11-24).

### A. Output Payload Size (`[CLI end-to-end]`, What Enters the Agent Context)

| Query Workload (`[CLI end-to-end]`) | `proxer search` | `rg` (ripgrep) | `grep -rn` | What happens in an agent loop |
| --- | --- | --- | --- | --- |
| **Broad exploration** (`func`, common term, ≥10k chunks) | **1.0–1.5 KB** (top 5 code files, 60-line chunks) | 4.8–7.3 MB (2k files)<br>18.4–29.2 MB (8k files) | 5.0 MB (2k files)<br>19.2 MB (8k files) | `rg`/`grep` flood stdout (~60k–240k lines); piping to `head` slices arbitrary alphabetical paths. |
| **Multi-term query** (`AuthMiddleware session`, rare co-occurrence) | **1.6 KB** (ranks co-occurring code #1) | **0 B / 0 hits** (treats as literal phrase) | **0 B / 0 hits** (treats as literal phrase) | `rg`/`grep` require extra turns to craft `AuthMiddleware.*session\|session.*AuthMiddleware`. |
| **Sub-token `camelCase`** (`middleware` → `AuthMiddleware`) | **1.5 KB** (matches `camelCase` tails, skips bundle) | **0 B** (default)<br>**256 KB / 1,234 lines** (`rg -i`) | **0 B** (default)<br>**259 KB / 1,234 lines** (`grep -rni`) | Default case-sensitive scan misses `AuthMiddleware`; `-i` dumps every doc match plus the 144 KB minified JS line. |
| **Path + content** (`pkg_039 AuthMiddleware`) | **1.5 KB** (path weight 5x + content match) | **0 B** with `-g "*pkg_039*"` (needs `**/*pkg_039*/**`) | **79 B** (via `find -path ... -exec grep`) | Proxer matches path tokens and code body in one query without shell glob traps. |
| **Rare exact symbol** (`verifySignatureToken7722`, 1 hit / ≤50 chunks) | **479–539 B** (includes 60-line chunk context) | **71 B** (single line only) | **74 B** (single line only) | When a symbol is unique and known, `rg`/`grep` return a smaller 1-line output; Proxer includes surrounding lines ready for `read`/`edit`. |

### B. Search & Index Latency (`[CLI end-to-end]`, Median of 7 Runs)

> **Historical note & §1B/§4 disagreement:** The `search func` row below (`55.2 / 81.9 ms` at 8,000 files) and [§4](#4-ranking--matching-ablation-cli-end-to-end) (`126.7 / 194.9 ms` at 8,000 files / 64,000 chunks) disagree by **2.3×** because §1B used an earlier, sparser pure-Go fixture (~32,000 chunks) before schema v2 `tails` indexing (`be5249a`), whereas §4 used a 64,000-chunk tree. Both predate commit `8b27a6a` (which added `count(*) OVER()` + CTE and regressed `search func` to `1,364 ms` in `v0.6.0`). Current post-`P2.1` rare + common-term numbers on the reproducible 72,000-chunk fixture are in [§5](#5-reproducible-audit-fixture--query-cost-ceiling-p21--2026-11-24).

| Operation (`[CLI end-to-end]`) | 2,000 files (17 MB) | 8,000 files (68 MB) | Notes |
| --- | --- | --- | --- |
| `proxer search` (rare token, ≤50 chunks, `--no-refresh`) | **8.7–11.6 ms** | **8.9–14.0 ms** | Direct SQLite FTS5 lookup |
| `proxer search` (rare token, ≤50 chunks, auto-refreshed) | **16.8–26.8 ms** | **38.6–71.9 ms** | Includes full-tree `stat` check before querying |
| `proxer search` (`camelCase` / multi-term, rare co-occurrence, `--no-refresh`) | **14.4–16.1 ms** | **15.3–20.4 ms** | `AND`-first with `OR` fallback + kind ranking |
| `proxer search` (`func`, pure Go tree, `--no-refresh` / refreshed) *(historical pre-`8b27a6a`)* | **20.8 / 27.8 ms** | **55.2 / 81.9 ms** | Superseded by §5 (`100.9 / 141.1 ms` on 72k chunks) |
| `rg` (rare token / multi-term / `-i` scan) | 32.5–33.5 ms | 127.1–134.8 ms | Linear filesystem scan on every call |
| `grep -rn` (rare token) | 189.3 ms | 791.6 ms | Single-threaded recursive scan |
| `python3` (`os.walk` + line scan) | 167.4 ms | 659.2 ms | Ad-hoc script often generated by agents |
| `proxer index` (first build from scratch) *(historical pre-schema-v2)* | **433 ms** | **1.87–2.00 s** | Current schema-v2 dense 72k-chunk build: `4.25 s` (see §5) |
| `proxer index` (no files changed) | **16.6–25.8 ms** | **35.6 ms** | Stat-only walk; opens no write transaction (post-`P1b.1`: `52.7 ms`, see §6) |
| `proxer index` (1,000 files changed) | **544 ms** | **717 ms** | ~0.7 ms per changed file |

---

## 2. Read: `proxer read` vs `sed -n` and `python3` (`[CLI end-to-end]`)

Agents inspect line slices before editing. Raw stream tools are fast on normal files but have no guard against minified or generated lines and provide no concurrency token.

| Workload (`[CLI end-to-end]`) | `proxer read` | `sed -n '20,50p'` | `python3` slice script | Why it matters |
| --- | --- | --- | --- | --- |
| **Normal source slice** (31 lines) | **11.1 ms · 1.0 KB** (JSON + SHA-256 `hash`) | **3.8 ms · 721 B** (raw text, no hash) | **27.4 ms · 721 B** (no hash) | `sed` is ~7 ms faster on raw stdout, but returns no whole-file `hash` to protect the subsequent edit. |
| **File with a 144 KB single line** (lines 1–5) | **11.8 ms · 4.4 KB** (caps line at 4,096 chars + ` [truncated]`) | **4.1 ms · 144.1 KB** (dumps full line) | **28.2 ms · 144.1 KB** (dumps full line) | Prevents a single minified bundle or embedded asset from blowing up the prompt while still returning the true file `hash`. |

---

## 3. Edit: `proxer edit` vs `sed -i` and `python3` (`[CLI end-to-end]`)

Agents typically mutate files by spawning `python3` rewrite scripts or running `sed -i`.

| Metric / Capability (`[CLI end-to-end]`) | `proxer edit --hash <sha256>` | `sed -i ''` | `python3` file-rewrite script |
| --- | --- | --- | --- |
| **Median latency** (2,000 / 8,000 files) | **15–19 ms** (includes single-file reindex) | **4.5–4.8 ms** | **28.0–28.3 ms** |
| **Stale-file protection** | **Yes** — rejects write if whole-file SHA-256 changed since `read` | No — overwrites blindly | No — overwrites blindly unless hand-rolled |
| **Multiline & escaping safety** | **Raw stdin** — no regex or delimiter escaping | Fragile (`/`, `\`, `&`, newline differences on BSD vs GNU `sed`) | Safe only if quote escaping inside `-c "..."` is clean |
| **Search index freshness** | **Automatic** — re-chunks only the edited file in the same call | None — leaves external indexes stale | None — leaves external indexes stale |

---

## 4. Ranking & Matching Ablation (`[CLI end-to-end]`)

Historical ablation (pre-`8b27a6a`) on generated pure-Go trees (**2,000 files / 16,000 chunks** and **8,000 files / 64,000 chunks**) comparing uniform FTS5 ranking against Proxer's weighted BM25 (`bm25(chunk_search, 0, 5, 0, 0, 1, 0.5)`), path matching, and soft kind ordering (`code` → `test` → `docs` → `assets`). See [§1B](#b-search--index-latency-cli-end-to-end-median-of-7-runs) for why the 64,000-chunk `search func` figure (`126.7 / 194.9 ms`) differs from §1B's sparser tree (`55.2 / 81.9 ms`).

| Workload (`[CLI end-to-end]`) | Uniform Baseline | Weighted BM25 + Kind Ordering |
| --- | --- | --- |
| `search func` (common term), 2,000 files / 16,000 chunks, `--no-refresh` | 47.3 ms | **43.7 ms** |
| `search func` (common term), 8,000 files / 64,000 chunks, `--no-refresh` | 156.4 ms | **126.7 ms** |
| `search func` (common term), same two fixtures, refreshed | 63.3 / 223.3 ms | **60.9 / 194.9 ms** |
| `search func -n 20` (common term), same two fixtures, `--no-refresh` | — | **42.3 / 127.5 ms** |
| `search file7722` (rare path-only token, 1 hit), `--no-refresh` | 0 hits | **1 hit** |
| `search func` where 50 docs fill the top BM25 window | 47.5 ms, five `doc*.md` | **78.1 ms, production code first** |

---

## 5. Reproducible Audit Fixture & Query Cost Ceiling (`P2.1` — 2026-11-24)

**Measurement stamp:** `2026-11-24` · commit `7303375` (post-`P2.1`, pre-`v0.7.0`) vs `v0.6.0` (`4c8ff1a`) · `macOS arm64 (Apple M1)` · `CGO_ENABLED=0` · `modernc.org/sqlite v1.62.0` · warm cache · median of 7 runs.

### Fixture Characteristics

- **Files:** `8,000` Go files across `80` packages (`pkg_000`–`pkg_079`, 100 files/pkg).
- **Line layout:** `376` lines/file (`package` + `import "context"` header + `62` 6-line functions/file), plus 1 unique rare symbol (`verifySignatureToken7722`) in `pkg_077/file_007722.go`.
- **Chunks:** `72,000` chunks (`9` chunks/file at the 60-line window / 15-line overlap).
- **Sizes & both DB-size denominators:**
  - **Raw source bytes:** `77.2 MB` (`~10,117 B/file`). Schema-v2 DB size is `172.8 MB` (`2.24×` raw source) on `v0.6.0` and `168.2 MB` (`2.18×` raw source) post-`P2.1`. Across Go/TS trees, DB size ranges **`~2.0–2.9×` raw source bytes** depending on comment/blank density and 60/15 overlap.
  - **Indexed chunk bytes:** `102.0 MB` (`~1,485 B/chunk` mean) in the `v0.6.0` audit (`1.69×` DB/chunk bytes) and `104.9 MB` (`1,528.1 B/chunk` mean) in the post-`P2.1` run (`1.60×` DB/chunk bytes). Relative to indexed chunk bytes, DB overhead stays tightly **`~1.6–1.7×`**.
- **Term occurrence counts (posting-list density across all 72,000 matching chunks):**
  - `func`: **9.1 / chunk** (**656,000** total occurrences)
  - `if`: **9.1 / chunk** (**656,000** total occurrences)
  - `context`: **18.3 / chunk** (**1,320,000** total occurrences)
  - `value`: **36.4 / chunk** (**2,624,000** total occurrences)
  - `verifySignatureToken7722` (rare): **1 matching chunk** (≤50 chunks)
- **Why these counts explain both query and cold-index cost:**
  - **Query cost** scales with **posting-list length (total occurrences)**, not prefix width: `if` and `func` match identically (`101.4 ms` vs `100.9 ms`) because both have `656,000` occurrences, whereas `context value` intersects `1.32M` + `2.62M` occurrences (`361.1 ms`).
  - **Cold index cost** (`4.25 s` for 77.2 MB / 72k chunks vs `1.87–2.00 s` in `v0.4.0` for 68 MB / 64k chunks) is ~2.1× slower because schema v2 (`be5249a`) added the `tails` sub-token column (`SplitIdentifier`), `471ec5e` added `snapChunkEnd`, and this dense fixture carries 62 functions/file.

### End-to-End Search Latency (`[CLI end-to-end]`, Rare + Common-Term Parity)

| Workload (`[CLI end-to-end]`) | Matching chunks · Posting-list density | Pre-`P2.1` (`v0.6.0`, `4c8ff1a`) | Post-`P2.1` (`7303375`, 2026-11-24) | Speedup |
| --- | --- | --- | --- | --- |
| `search verifySignatureToken7722` (rare; `--no-refresh` / refreshed) | 1 chunk (≤50) · 1 occ. | 9.1 / 47.5 ms | **10.3 / 50.3 ms** | — |
| `search func` (common; `--no-refresh` / refreshed) | 72,000 chunks · 656k occ. (9.1/chunk) | 1,364 / 1,389 ms | **100.9 / 141.1 ms** | **13.5× / 9.8×** |
| `search if` (common; `--no-refresh` / refreshed) | 72,000 chunks · 656k occ. (9.1/chunk) | 1,273 ms / — | **101.4 / 141.1 ms** | **12.6×** |
| `search "context value"` (common; `--no-refresh` / refreshed) | 72,000 chunks · 1.32M + 2.62M occ. (18.3 + 36.4/chunk) | 3,422 ms / — | **361.1 / 402.3 ms** | **9.5×** |

### In-Process SQL Breakdown (`[in-process modernc.org/sqlite SQL]`, 72,000 matching chunks)

Measured on the same 72,000-chunk fixture (`CGO_ENABLED=0`, `modernc.org/sqlite v1.62.0`, `2026-11-23`) without CLI startup or refresh walk:

| Operation (`[in-process modernc.org/sqlite SQL]`) | Time |
| --- | --- |
| `database.Open` | **0.18 ms** |
| `SELECT count(*) FROM chunk_search WHERE chunk_search MATCH '"func"*'` (656k occ.) | **6.4 ms** (`"if"*`: **6.5 ms**; `"context"* AND "value"*`: **13.6 ms**) |
| Direct query without `OVER()`/CTE (`func`, with `snippet` + `content`, `LIMIT 200`) | **84.4 ms** (`if`: **84.7 ms**) |
| Direct query without `OVER()`/CTE (`context AND value`, with `snippet` + `content`, `LIMIT 200`) | **155.3 ms** |

### Reproducible Fixture Recipe (Run by Hand)

No generator or `make bench` target is committed to the repo. Rebuild and measure the fixture from scratch with a fresh binary and fresh `.proxer.db`:

```bash
# 1. Build a fresh CGO-free binary
CGO_ENABLED=0 go build -o /tmp/proxer-bench ./cmd/proxer

# 2. Generate the 8,000-file / 72,000-chunk Go fixture (62 6-line funcs/file, 376 lines/file)
FIXTURE=/tmp/proxer-audit-8k
rm -rf "$FIXTURE" && mkdir -p "$FIXTURE"
python3 - "$FIXTURE" <<'PY'
import os, sys
root = sys.argv[1]
for pkg in range(80):
    d = os.path.join(root, f"pkg_{pkg:03d}")
    os.makedirs(d, exist_ok=True)
    for i in range(100):
        idx = pkg * 100 + i
        lines = [f"package pkg_{pkg:03d}", "", 'import "context"', ""]
        for fn in range(62):
            name = f"HandleItem{idx:05d}_{fn:02d}"
            if idx == 7722 and fn == 0:
                name = "verifySignatureToken7722"
            lines.extend([
                f"func {name}(ctx context.Context, value string) string {{",
                f"\tif value == \"\" || ctx.Err() != nil {{",
                f"\t\treturn value",
                f"\t}}",
                f"\treturn value + \":\" + value",
                "}",
            ])
        with open(os.path.join(d, f"file_{idx:06d}.go"), "w") as f:
            f.write("\n".join(lines) + "\n")
PY

# 3. Cold index on a fresh DB
rm -f "$FIXTURE"/.proxer.db*
/usr/bin/time -p /tmp/proxer-bench index --root "$FIXTURE"

# 4. Run rare + common-term queries (warm cache; take median of 7 runs)
for q in "verifySignatureToken7722" "func" "if" "context value"; do
  /tmp/proxer-bench search --root "$FIXTURE" --no-refresh -k "$q" >/dev/null
  /tmp/proxer-bench search --root "$FIXTURE" -k "$q" >/dev/null
done
```

---

## 6. No-Op Refresh Walk Verification (`P4.1` — 2026-11-24)

**Measurement stamp:** `2026-11-24` · commit `67015dd` (`P4`) · `macOS arm64 (Apple M1)` · `CGO_ENABLED=0` · `modernc.org/sqlite v1.62.0` · warm cache · median of 7 runs on the 8,000-file / 72,000-chunk fixture after `P1b.1` (`.gitignore` parse-once-per-refresh):

| Workload (`[CLI end-to-end]`) | Measured 2026-11-24 | Gate | Result |
| --- | --- | --- | --- |
| `proxer index` (no files changed) | **52.7 ms** | `< 80 ms` | Verified (single-threaded `computeDiff` retained) |
| `proxer search` (rare symbol, auto-refreshed) | **56.8 ms** | `< 80 ms` | Verified |

---

## When Raw CLI Tools Win (Trade-Offs)

Responsible numbers mean being clear about where `rg`, `grep`, or `sed` remain better fits:
- **Known rare symbol, 1-line check:** If you already know an identifier appears <5 times and only want `file:line` without surrounding lines, `rg` or `grep -n` outputs ~70–400 B vs Proxer's ~500 B–1.2 KB JSON chunk envelope.
- **Full-regex or arbitrary substring sweeps:** Proxer uses FTS5 token-prefix + `camelCase` tail indexing. For punctuation-heavy regex refactoring across all files, use `rg`.
- **Cold one-off query on a massive repo:** If a repository has no `.proxer.db` yet and you only ever run a single query, `rg` (~130 ms at 8,000 files) avoids the initial ~4.25 s dense index build. From the second query onward, Proxer's incremental `stat` refresh (~53 ms) and FTS5 lookup (~10–101 ms) amortize the build cost.

## Methodology

- **Hardware & toolchain:** `macOS arm64 (Apple M1)`, warm filesystem cache, `CGO_ENABLED=0`, `modernc.org/sqlite v1.62.0`.
- **Freshness rule:** Every benchmark run uses a freshly built binary and a newly created `.proxer.db` (never reusing a DB from an older build).
- **Timing:** Median of 7 runs per command, full process startup included for `[CLI end-to-end]`.
- **Baselines:** `rg` (ripgrep `15.2.0`), macOS BSD `grep` and `sed`, and CPython `python3` via `subprocess`.
