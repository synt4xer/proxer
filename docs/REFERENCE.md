# CLI & Configuration Reference

Reference for Proxer's installation, root/database resolution, CLI commands, JSON schemas, `.proxer.json`, and limitations. For Agent Skill and MCP server setup, see [INTEGRATION.md](INTEGRATION.md).

---

## 1. Build & Install

```bash
make test
make build
make install DEST=/path/to/your-project
```

- `make build` produces a static CGO-free binary (`./bin/proxer`) using `modernc.org/sqlite`.
- `make install DEST=...` copies `SKILL.md` and the binary into `<DEST>/.agents/skills/proxer/` without modifying `.gitignore` or project configs. Add `.proxer.db*` to `.gitignore` or `.git/info/exclude` if desired.
- For a user-level install shared across repos, run `make install DEST=$HOME`.

---

## 2. Root & Database Resolution

Every command resolves the project root in this order:

```
--root <path>  →  PROXER_ROOT  →  working directory
```

Database location (`--db` / `PROXER_DB`) resolves by install mode:
1. **Override:** `--db <path>` or `PROXER_DB` if set.
2. **Project mode:** `<root>/.proxer.db` when `.agents/skills/proxer/` or `.proxer.db` exists in `<root>` (or for standalone CLI invocations).
3. **Harness mode:** `<skillDir>/database/<repo>-<hash12>.db` when invoked from a user-level skill directory (`~/.agents/skills/proxer/`, `PROXER_SKILL_DIR`) or `proxer mcp` without a project-local DB.

All `-f` file paths resolve relative to `<root>`. Paths escaping `<root>` (including via symlinks) are rejected.

---

## 3. CLI Commands

All commands print JSON to stdout and write errors to stderr with a non-zero exit code. Global flags: `--root <path>`, `--db <path>`, `--version`.

| Command | Summary |
| --- | --- |
| `index` | Incrementally update the index (new, changed, deleted files; skips minified blobs, `.gitignore` matches, and sensitive files by default). |
| `search -k <query> [-n 1..50] [--kind all\|code\|docs] [--no-refresh]` | Auto-refresh the index, then return up to `limit` distinct files (default `5`) ranked `code` → `test` → `docs` → `assets`. |
| `read -f <file> [-s 1] [-e 60] [-n\|--numbered]` | Return an inclusive line range (4,096-char line cap, 64 KB response cap) plus whole-file SHA-256 `hash`. |
| `edit -f <file> (-s <start> -e <end> \| --old <text> [--occurrence N]) [--hash <sha256>]` | Replace a line range or exact anchor block with stdin content, write atomically, and reindex only that file. |
| `status` (alias `doctor`) `[--vacuum] [--prune]` | Inspect index metadata, DB size/permissions, ignore sources, and optional maintenance (`--vacuum`, `--prune` orphaned harness DBs). |
| `mcp` | Serve `index`, `search`, `read`, `edit`, and `status` over MCP stdio. |

### `index`

```bash
proxer index
```

Incremental by default: files matching cached size + mtime are skipped; unchanged hashes are only re-stamped; deleted paths are removed. You do not need to run `index` before `search` unless you want to pay the initial build cost up front.

### `search`

```bash
proxer search -k "verifySignatureToken7722"
proxer search -k "auth session" -n 10 --kind code
```

Example response:

```json
{
  "root": "/path/to/repo",
  "indexed_at": "2026-11-24T11:04:17.976384Z",
  "refreshed": {"added": 0, "changed": 0, "removed": 0, "skipped": 0},
  "total": 1,
  "truncated": false,
  "results": [
    {
      "root": "/path/to/repo",
      "filepath": "pkg_077/file_007722.go",
      "chunk": "1-58",
      "line": 5,
      "snippet": "func <b>verifySignatureToken7722</b>(ctx context.Context, value string) string {",
      "match": "content",
      "hash": "ffa74c66..."
    }
  ]
}
```

Key behavior:
- **Matching:** Token-prefix matching (`auth` → `AuthMiddleware`), `camelCase`/`PascalCase` tail indexing (3+ chars on code/config, so `Signature` → `verifySignatureToken7722`), `AND`-first with `OR` fallback, 5× path weight, and automatic FTS5 punctuation sanitization.
- **Fields:** `chunk` is the 60-line window (`15`-line overlap); `line` is the matched line number when available; `match` is `path`, `content`, or `both`; `results` is always an array (`[]` when empty).
- **Read-only / pinned queries:** Pass `--no-refresh` or set `PROXER_NO_REFRESH=1` to skip the filesystem walk (`refreshed` is omitted). If the DB is read-only on disk, `search` falls back to read-only mode and sets `"stale": true`.

### `read`

```bash
proxer read -f "pkg_077/file_007722.go" -s 1 -e 10 --numbered
```

Returns:

```json
{
  "root": "/path/to/repo",
  "filepath": "pkg_077/file_007722.go",
  "start_line": 1,
  "end_line": 10,
  "total_lines": 376,
  "truncated": false,
  "next_start": 11,
  "content": "...",
  "hash": "ffa74c66..."
}
```

Lines >4,096 chars are capped with ` [truncated]`, and total output is capped at `64 KB` (setting `truncated: true` and `next_start`). `hash` always covers the full file on disk.

### `edit`

Line-range mode:

```bash
proxer edit -f "internal/auth.go" -s 52 -e 58 --hash "1f0c..." <<'EOF'
func AuthMiddleware(next http.Handler) http.Handler {
	return requireSession(next)
}
EOF
```

Anchor mode (mutually exclusive with `-s`/`-e`):

```bash
proxer edit -f "internal/auth.go" --old "return oldHandler(next)" --hash "1f0c..." <<'EOF'
return requireSession(next)
EOF
```

- Writes atomically via temp file + rename and reindexes **only** the edited file.
- `--hash` rejects the write if the file changed since `read` or `search`.
- Refuses edits targeting truncated lines (>4,096 chars) or containing ` [truncated]`.
- Returns `{"status": "...", "message": "...", "hash": "<new-sha256>"}`.

### `status` (`doctor`)

```bash
proxer status
proxer status --vacuum
proxer status --prune
```

Reports `db_path`, `db_size_bytes`, `db_mode`, `schema_version`, `indexed_at`, `files_indexed`, `chunks_indexed`, `ignore_sources`, `install_mode`, and any `orphan_databases`. `--vacuum` runs FTS5 optimize, `VACUUM`, and WAL checkpoint; `--prune` removes orphaned harness DBs whose project root no longer exists.

---

## 4. Configuration (`.proxer.json` & Environment)

Optional `<root>/.proxer.json` (strictly decoded; malformed JSON or invalid entries fail fast):

```json
{
  "ignored_dirs": ["third_party", "generated"],
  "index_sensitive": false
}
```

- `ignored_dirs`: Single directory names or base-name globs (e.g. `*.egg-info`) merged with `config.DefaultIgnoredDirs` and `.gitignore`.
- `index_sensitive`: Defaults to `false` (skips `.env`, keys, certs, and secret files). Set to `true` only if you intentionally want sensitive files indexed.

Environment variables:

| Variable | Effect |
| --- | --- |
| `PROXER_ROOT` | Project root fallback when `--root` is omitted |
| `PROXER_DB` | Explicit DB path override when `--db` is omitted |
| `PROXER_NO_REFRESH` | Truthy value (`1`, `true`, `yes`) disables pre-search refresh |
| `PROXER_SKILL_DIR` | Override user-level skill directory for harness DB routing |

---

## 5. Limitations

- **Project scope:** `read` and `edit` reject paths outside `<root>`.
- **No regex or globs:** `search` uses FTS5 token-prefix + `camelCase` tail indexing, not regex.
- **Stat-gated refresh:** Same-size writes within the same filesystem mtime tick are skipped until another change occurs.
- **Indexed allowlist:** Files outside `config.AllowedExtensions` / `config.AllowedExactNames` or >1 MB are not searchable (`read` and `edit` still work on them).
- **Existing files only:** `edit` modifies existing files; creating/deleting files is out of scope.
- **Not a semantic analyzer:** Pair with an LSP (`gopls`, `tsserver`, `rust-analyzer`) for type checking and call graphs.

---

## 6. Release Gate Checklist

Every release tag (`v*`, enforced by `.github/workflows/ci.yml`) must pass:

1. `test -z "$(gofmt -l .)"`
2. `go vet ./...`
3. `CGO_ENABLED=0 go build ./...`
4. `CGO_ENABLED=0 go test ./...` and `go test -race ./...`
5. Fresh benchmark run on a fresh binary + fresh `.proxer.db` (see [BENCHMARKS.md](BENCHMARKS.md) §6)
6. Update verification entry in [INTEGRATION.md](INTEGRATION.md)
7. Bump `config.Version` (`internal/config/config.go`) per semver and publish GitHub Release notes
