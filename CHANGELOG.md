# Changelog

Notable changes to Proxer, newest first. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html): a backward-compatible feature is a MINOR bump, a fix is a PATCH, and a documentation-only change gets no tag.

Each entry describes what that tag changed relative to the tag before it. Two release traits are worth knowing before reading:

- Tags `v0.2.1` through `v0.2.3` were cut from the same development line that `v0.3.0` later spans, and have no release page of their own.
- `.proxer.db` carries a schema stamp and the chunk window. An upgrade that changes either one clears and rebuilds the database once on first use rather than reading rows the new binary cannot account for.

## [Unreleased]

### Added

- `CHANGELOG.md`, so the repository states what each tag contains without requiring a reader to reconstruct it from the log.

### Changed

- `README.md` re-measured for v0.4.0: the benchmark table is now one coherent seven-run measurement over generated Go trees (2,000 files / 17 MB and 8,000 files / 68 MB), the current release is named and linked, and an "Upgrading from v0.3.0" section explains the one-time rebuild and the `search` envelope.
- `docs/INTEGRATION.md` states the release it documents and records the two cases a caller otherwise discovers at runtime: `results` is `[]` rather than null when nothing matches, and `refreshed` is absent under `PROXER_NO_REFRESH=1`.

## [0.4.0] - 2026-11-16

Freshness moved from the agent's memory to the tool. A file written by a harness tool, by `git pull`, or by another process is found by the next `search`, and a project with no database is indexed on the first query instead of answered with `[]`.

### Added

- `search` refreshes the index before it queries. Files whose size and modification time still match the manifest are skipped, files whose bytes hash the same are only re-stamped, and the rows of deleted paths are dropped — in one transaction, and in none when nothing changed. A refresh with nothing to do opens no write transaction at all.
- `index` runs the same delta build, so running it again is a stat-only walk.
- `--no-refresh` on `search`, and `PROXER_NO_REFRESH=1` for an MCP server that must not write: answer from the index as it stands and omit `refreshed` from the response.
- `search` results carry `indexed_at` and a `refreshed` count of added, changed, and removed files, so the caller sees the index move instead of guessing.
- `database/` package: connection, DSN, pragmas, schema stamp, chunk rows, the rowid map, and the per-file manifest. `internal/indexer/` keeps policy — the walk, the diff, and what to chunk.
- `indexed_files` manifest (`size`, `mtime_ns`, `hash`) and a `meta` table (`schema_version`, `chunk_size`, `overlap_size`, `indexed_at`), so a database built by an older binary is rebuilt once instead of extended with rows it cannot account for.

### Changed

- **Breaking for scripts:** `search` prints an envelope instead of a bare array. Read `results` where the top-level array used to be; `index`, `read`, and `edit` are unchanged.
- A single-file delete goes through the `index_chunks(rowid, filepath)` map: 0.05 ms per file at 16,100 chunks, against 7.6 ms for `DELETE ... WHERE filepath = ?`, which had no secondary index and scanned the FTS5 table.
- `edit` writes the chunk rows and the manifest row in the same transaction, so the two records cannot drift.
- The first build costs more because it hashes every file and writes the rowid map: 2.00 s at 8,000 files / 32,000 chunks, where the pre-P0 build of a smaller fixture (8,000 files / 63 MB / 16,000 chunks) took 1.34 s. A later build pays only for what changed.

### Fixed

- A database rebuilt for a new schema or chunk window kept the old `indexed_at`, so `search` reported a timestamp for rows that no longer existed. The wipe now clears it along with the rows.

### Known limitations

- Freshness is gated on size and modification time. A same-size write inside one timestamp tick is not noticed until the file changes again; every case where the gate opens is confirmed against the content hash before re-chunking.

## [0.3.0] - 2026-11-16

### Added

- Per-project `.proxer.json` with `ignored_dirs`, layered over the built-in skip list, so a project adds directories without losing the defaults.
- Test coverage for the project skip list.

### Changed

- The indexer takes the ignored directories as a parameter instead of reading configuration itself.
- `README.md` and `skills/proxer/SKILL.md` describe the project ignore config.

## [0.2.3] - 2026-11-16

### Added

- `read` returns a SHA-256 `hash` of the whole file.

### Changed

- `edit` accepts that `hash` and refuses the edit when the file changed in between, instead of overwriting code the model has not seen.

## [0.2.2] - 2026-11-16

### Fixed

- JSON output no longer HTML-escapes `<`, `>`, and `&`, so a snippet shows angle brackets as text instead of `\u003cb\u003e`.
- The search test passes the project root instead of relying on the working directory.

## [0.2.1] - 2026-11-16

### Added

- `search` and `read` results carry the absolute `root` they came from, so a harness driving several projects can tell which index a path belongs to.

### Changed

- `README.md`, `docs/INTEGRATION.md`, and `skills/proxer/SKILL.md` list the field.

## [0.2.0] - 2026-11-16

The first release with a working CLI. The `v0.1.0` tag is scaffolding only.

### Added

- `index`, `search`, `read`, and `edit` over a SQLite FTS5 index in `.proxer.db`, chunked into 60-line windows with a 15-line overlap.
- `edit` reindexes only the file it wrote, so the cost tracks the file rather than the project.
- Project root resolution: `--root`, then `PROXER_ROOT`, then the working directory.
- Prefix matching per search term, with quotes and FTS5 operators sanitized before they reach the parser.
- `proxer mcp`: index, search, read, and edit as stdio MCP tools, which a harness exposes as `mcp__proxer__<tool>`.
- Agent Skill package under `skills/proxer/`, placed in a project by `make install`.
- `docs/INTEGRATION.md` for harness setup.

### Fixed

- `read` and `edit` refuse a path that resolves outside the project root instead of keying chunks under it.
- `edit` replaces the file atomically, so an interrupted write cannot leave a truncated source file.
- Concurrent writers queue on the busy timeout instead of failing with `SQLITE_BUSY`.
- A failed pragma is reported instead of panicking.

## [0.1.0] - 2026-11-15

### Added

- Project scaffolding: `README.md`, `LICENSE`, `Makefile`, `.editorconfig`, `.gitignore`, and the Go module.

Scaffolding is all this tag contains; `make build` needs a tree from `v0.2.0` or later.

[Unreleased]: https://github.com/synt4xer/proxer/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/synt4xer/proxer/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/synt4xer/proxer/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/synt4xer/proxer/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/synt4xer/proxer/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/synt4xer/proxer/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/synt4xer/proxer/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/synt4xer/proxer/releases/tag/v0.1.0
