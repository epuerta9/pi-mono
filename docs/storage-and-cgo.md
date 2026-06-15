# Storage & cgo

> Picking an embeddable store for a harness **distributed as a CLI and run by
> users**, the SQLite single-writer problem, and what cgo vs pure-Go actually means.

> [!abstract] The real lesson
> Don't force one DB to do everything. The harness has **three distinct storage
> workloads**, and the right answer differs per workload. SQLite's single-writer
> pain usually shows up because one store is being asked to do a job it's wrong for.

| Workload | Shape | Right tool |
|---|---|---|
| Session / conversation | append-only, 1 process, 1 writer | **JSONL** (what pi uses) — simple, human-readable, no DB |
| Trace / observability | write-heavy append + analytical reads | **SQLite-WAL** (pure-Go) or **Parquet/DuckDB** for heavy analytics |
| Concurrent / shared / mesh state | many writers, possibly cross-process/host | **NATS JetStream KV** (mesh) or **Badger** (local) — *not* a file DB |

---

## 1. The SQLite single-writer truth (and the fix)

SQLite serializes writes: **one writer at a time**, full stop. WAL mode does **not**
add concurrent writers — it makes *writes not block reads and reads not block
writes* (concurrent readers + one writer). So the "it sucked for concurrent work"
experience is real, but it's usually **architecture, not a hard ceiling**:

- **WAL mode** on (`PRAGMA journal_mode=WAL`).
- **`busy_timeout`** set (e.g. 5000ms) — without it, a contended write fails
  *instantly* with `SQLITE_BUSY` instead of waiting. This single pragma fixes most
  "database is locked" pain.
- **`BEGIN IMMEDIATE`** for write txns — don't let a read txn upgrade to a write
  (that path ignores `busy_timeout`).
- **Single-writer-goroutine pattern**: one goroutine owns all writes (serialize
  through a channel), many reader connections in a separate pool. "One writer, many
  readers, enforced in code."

Done this way, SQLite backs a concurrent API fine **within one process**. Where it
genuinely breaks down: **multiple separate processes writing the same file** (file-
lock contention) — i.e., exactly a distributed/mesh scenario. That's the signal to
leave file DBs behind.

> [!note] If you want SQLite ergonomics *and* real concurrent writes
> **Turso / libSQL** (SQLite fork) adds `BEGIN CONCURRENT` — multiple concurrent
> writers in WAL, ~4× write throughput, no `SQLITE_BUSY`. Caveat: it's C/Rust, not
> pure Go (see §3).

---

## 2. Embeddable options for a CLI binary

| Store | Pure Go? | Concurrency | SQL? | Notes |
|---|---|---|---|---|
| **modernc.org/sqlite** | ✅ yes | 1 writer / many readers (WAL) | ✅ | SQLite transpiled to Go; **static binary preserved**; slower than cgo SQLite |
| mattn/go-sqlite3 | ❌ cgo | same | ✅ | faster, but breaks cross-compile/static (§3) |
| **bbolt** | ✅ yes | 1 writer / many readers (MVCC) | ✗ (KV) | rock-solid, simple, append/KV; same single-writer model |
| **Badger** | ✅ yes | **concurrent ACID txns**, write-optimized (LSM) | ✗ (KV) | best pure-Go *write* concurrency; bigger footprint |
| **DuckDB** | ❌ cgo (C++) | single-process OLAP | ✅ | superb for "compare runs" analytics; doesn't fix concurrency; cgo tax |
| Turso / libSQL | ❌ C/Rust | **concurrent writers** | ✅ | SQLite + `BEGIN CONCURRENT` |
| embedded-postgres | ❌ (ships a server) | true MVCC multi-writer | ✅ | downloads/launches real Postgres — too heavy for a CLI default |
| NATS JetStream KV | ✅ (Go) | distributed, replicated | ✗ (KV) | the mesh answer for shared state |

---

## 3. cgo vs pure Go (why it matters *here*)

- **cgo** = Go calling C/C++. Required by any package binding a C library
  (`mattn/go-sqlite3` → SQLite C, DuckDB → C++). Costs:
  - needs a **C toolchain** to build;
  - **cross-compilation is painful** — `GOOS=linux GOARCH=arm64 go build` no longer
    "just works"; you need a matching C cross-compiler per target;
  - binaries often **dynamically link libc** (not fully static → glibc-version
    breakage across distros);
  - small per-call FFI overhead; slower builds; messier CI.
- **pure Go** (`CGO_ENABLED=0`) = **one fully static binary**, trivially
  cross-compiled to every `GOOS/GOARCH`, no external toolchain.

> [!warning] cgo taxes the core value prop
> This harness's whole pitch is *"single static binary, cross-compile everywhere,
> users run a CLI."* Every cgo dependency erodes that. **Default to pure-Go stores.**
> Pay the cgo tax only when the gain is decisive (e.g., DuckDB analytics), and
> isolate it behind a build tag / optional feature so the default build stays clean.

`modernc.org/sqlite` is the sweet spot: real SQLite, **pure Go**, static binary and
cross-compile intact — at some performance cost vs the cgo driver (fine at this scale).

---

## 4. Decision for the harness

- **Sessions** → JSONL (append-only; no DB; matches pi).
- **Local trace store** → `modernc.org/sqlite` (pure Go) + WAL + `busy_timeout` +
  single-writer goroutine. One harness process = one writer → single-writer is a
  non-issue here. Use **Parquet + DuckDB** *only if* run-comparison analytics demand
  it, behind an optional build tag (cgo).
- **High-write KV / no SQL needed** → **Badger** (pure Go, concurrent txns).
- **Concurrent / cross-process / mesh state** → **NATS JetStream KV** — don't make a
  local file DB do a distributed job. This is the lesson from the single-writer pain:
  the moment writers are *separate processes*, leave file DBs behind.
