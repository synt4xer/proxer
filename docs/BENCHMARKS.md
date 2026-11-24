# Benchmarks & Performance

All measurements below come from a single fresh build of the current codebase. No numbers from earlier builds or legacy fixtures are carried over.

**Environment:** `macOS 26.5.1 arm64 (Apple M1)` · `Go 1.27.4` (`CGO_ENABLED=0`) · `modernc.org/sqlite v1.62.0` · warm cache · median of 7 runs (after 1 warmup) · `[CLI end-to-end]` (full subprocess startup, SQLite open, query/refresh, JSON stdout).

---

## 1. Fixture

- **Tree:** `8,000` Go files across `80` packages (`pkg_000`–`pkg_079`, 100 files/pkg, `376` lines/file, `73.11 MB` raw Go) + `1` single-line minified JS file (`bundle.min.js`, `143.6 KB`).
- **Index result:** `72,000` chunks (`9` chunks/file at 60-line windows with 15-line overlap), `bundle.min.js` automatically skipped (`skipped: 1`).
- **Database size:** `156.95 MB` (`2.15×` raw Go source bytes).
- **Posting-list density across all 72,000 chunks:**
  - `func`: `656,000` occurrences (`9.1/chunk`)
  - `if`: `656,000` occurrences (`9.1/chunk`)
  - `context`: `1,320,000` occurrences (`18.3/chunk`)
  - `value`: `2,624,000` occurrences (`36.4/chunk`)
  - `verifySignatureToken7722`: `1` occurrence (`1` matching chunk)

---

## 2. Indexing (`[CLI end-to-end]`)

| Operation | Latency | Notes |
| --- | --- | --- |
| `proxer index` (cold build, fresh DB) | **5.43 s** | `8,000` Go files (`72,000` chunks), `1` minified file skipped |
| `proxer index` (no files changed) | **63.5 ms** | Stat-only walk; no write transaction |
| `proxer index` (100 files changed) | **481.4 ms** | ~4.8 ms per changed file (re-chunk + FTS5 update) |

---

## 3. Search: `proxer search` vs `grep -rn` and `python3` (`[CLI end-to-end]`)

| Workload | `proxer search` (`--no-refresh` / refreshed) | `proxer` payload | `grep -rn` | `python3` walk |
| --- | --- | --- | --- | --- |
| **Rare symbol** (`verifySignatureToken7722`, 1 chunk) | **11.4 ms / 64.6 ms** | **519 B / 579 B** (chunk + `hash`) | 1,612.9 ms · **120 B** | 914.3 ms · **120 B** |
| **Sub-token `camelCase`** (`Signature` → `verifySignatureToken7722`) | **11.5 ms / 62.8 ms** | **519 B / 579 B** (1 hit) | 0 hits (case-sensitive) | — |
| **Common term** (`func`, 72k chunks · 656k occ.) | **146.2 ms / 204.2 ms** | **2.1 KB** (top 5 files) | 1,311.2 ms · **54.88 MB** | — |
| **Common term** (`if`, 72k chunks · 656k occ.) | **150.8 ms / 205.3 ms** | **2.1 KB** (top 5 files) | Unbounded line flood | — |
| **Common multi-term** (`context value`, 72k chunks · 1.32M + 2.62M occ.) | **271.1 ms / 327.4 ms** | **2.3 KB** (top 5 files, `AND`-first) | 1,641.7 ms · **0 B** (0 hits, literal phrase) | — |

Query cost scales with posting-list length: `func` and `if` take ~146–151 ms because both have `656,000` occurrences, whereas `context value` intersects `1.32M + 2.62M` occurrences (~271 ms). Payload size stays bounded at ~0.5–2.4 KB regardless of match volume.

---

## 4. Read & Edit: `proxer` vs `sed` and `python3` (`[CLI end-to-end]`)

| Operation | `proxer` | `sed` | `python3` | Trade-off |
| --- | --- | --- | --- | --- |
| **Read 31-line Go slice** | **9.2 ms · 1.1 KB** (JSON + SHA-256 `hash`) | **3.2 ms · 778 B** | 26.7 ms · 778 B | `sed` is ~6 ms faster, but provides no `hash` or truncation metadata. |
| **Read 143.6 KB single-line file** (lines 1–5) | **9.4 ms · 4.3 KB** (caps line at 4,096 chars + ` [truncated]`) | 4.2 ms · **143.6 KB** | 27.6 ms · **143.6 KB** | Proxer prevents minified lines from blowing up the prompt while returning the full-file `hash`. |
| **Edit 6-line function** | **19.6 ms** (`--hash` guard + atomic write + single-file reindex) | **4.1 ms** (`sed -i`, blind overwrite) | 27.0 ms (blind overwrite) | `sed -i` has no stale-file check, fragile escaping, and leaves search indexes stale. |

---

## 5. When Other Tools Win

- **Cold one-off query on an unindexed repo:** Building the initial index on 72k dense Go chunks takes `5.43 s`. If you will only run one query and never touch the repo again, a direct scanner (`rg`, `grep`, or harness `Grep`) avoids the upfront build.
- **Regex, glob, or punctuation-heavy sweeps:** Proxer uses FTS5 token-prefix + `camelCase` tail indexing, not regex. Use `rg` or harness `Grep` for regex refactoring.
- **Known rare symbol (<5 hits) when you only need `file:line`:** Raw `grep -n` outputs `120 B` vs Proxer's `519 B` JSON envelope (which includes snippet context and file `hash`).

---

## 6. Reproduce Locally

Build a fresh CGO-free binary and generate a fresh fixture + `.proxer.db` (never reuse a DB from an older build):

```bash
# 1. Build a fresh CGO-free binary
CGO_ENABLED=0 go build -o /tmp/proxer-bench ./cmd/proxer

# 2. Generate the 8,000-file / 72,000-chunk Go fixture + 143.6 KB minified file
FIXTURE=/tmp/proxer-bench-8k
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
                '\tif value == "" || ctx.Err() != nil {',
                "\t\treturn value",
                "\t}",
                '\treturn value + ":" + value',
                "}",
            ])
        with open(os.path.join(d, f"file_{idx:06d}.go"), "w") as f:
            f.write("\n".join(lines) + "\n")
with open(os.path.join(root, "bundle.min.js"), "w") as f:
    f.write(("var a=function(x){return x+1};" * 4900) + "\n")
PY

# 3. Cold index on a fresh DB
rm -f "$FIXTURE"/.proxer.db*
/usr/bin/time -p /tmp/proxer-bench index --root "$FIXTURE"

# 4. Run rare, camelCase, and common-term queries (warm cache; take median of 7 runs)
for q in "verifySignatureToken7722" "Signature" "func" "if" "context value"; do
  /tmp/proxer-bench search --root "$FIXTURE" --no-refresh -k "$q" >/dev/null
  /tmp/proxer-bench search --root "$FIXTURE" -k "$q" >/dev/null
done
```

Baselines measured with macOS BSD `grep 2.6.0-FreeBSD`, BSD `sed`, and CPython `3.15.2`.
