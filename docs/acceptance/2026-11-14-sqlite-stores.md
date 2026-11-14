# SQLite: the three answers the spec owed

2026-11-14 (Brisbane). Spec, section 17, the row "The `synchronous` level the SQLite driver uses by default; its `_txlock` option for `BEGIN IMMEDIATE`" and "`fullfsync` in the darwin build". Driver: `modernc.org/sqlite` v1.60.1, which carries SQLite 3.53.4. Measured on an M4 Pro, macOS 27.2, Go 1.27.2, `darwin/arm64`. Not yet run on the mini or on a GitHub runner.

## 1. The `synchronous` level the driver uses by default

**FULL (2)**, in WAL as in the rollback journal. The driver's build sets `SQLITE_DEFAULT_SYNCHRONOUS` and `SQLITE_DEFAULT_WAL_SYNCHRONOUS` to 2, and a connection opened with no setting reads back 2 with and without `journal_mode(WAL)`.

labd names the level anyway (`synchronous(FULL)`), so a later driver with another default changes nothing. In WAL, FULL syncs the log at every commit: a commit that returned is on the drive.

Proof: `go test ./internal/sqlitedb -run TestTheDriversDefaultSynchronousLevel` and `-run TestSettingsReadBackAsSet`.

## 2. `_txlock` for `BEGIN IMMEDIATE`

**It exists and works.** `_txlock=immediate` in the connection string makes every transaction that is not read-only start with `begin immediate` (the driver's `tx.go`; `deferred`, the default, and `exclusive` are the other values). Both stores open their one write connection with it.

Proof: while a write transaction is open and has run no statement, a second connection's write is refused at once with `SQLITE_BUSY`; with the option removed the same write goes through. `go test ./internal/sqlitedb -run TestWriteTransactionsStartImmediate`, and `./internal/server -run TestStoreWriteTransactionsStartImmediate` for `labd.db`.

## 3. `fullfsync` in the darwin build

**It takes effect.** Three things show it:

- `PRAGMA fullfsync` reads back 1 on the write connection, with `synchronous` 2 and `journal_mode` `wal` (`TestSettingsReadBackAsSet`).
- The driver's darwin source calls `fcntl(fd, F_FULLFSYNC)` when the flag is set and falls back to `fsync` only if that fails; its Linux source has `fsync` alone.
- A commit costs what a write to the drive itself costs. One row a commit, 300 commits:

```
$ go test -run '^$' -bench Commit -benchtime 300x ./internal/sqlitedb
goos: darwin
cpu: Apple M4 Pro
BenchmarkCommit/fullfsync=1-14         	     300	   3546151 ns/op
BenchmarkCommit/fullfsync=0-14         	     300	     68784 ns/op
```

About 3.5 ms a commit with `fullfsync`, 0.07 ms without: some 280 commits a second through one file. labd's write rate is far under that.

On Linux the pragma reads back 1 and does nothing more; there `fsync` already reaches the drive.

## What was not checked

- The same benchmark on the mini's own drive (the Deploy of slice 04 can run it: it needs only the test binary).
- Litestream beside labd, and Full Disk Access for LaunchDaemons: the two other halves of the same rows of section 17, which belong to the mini's set-up.
