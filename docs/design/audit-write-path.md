# Design note: the audit write path

Status: implemented as an opt-in (`--audit-queue`). The default is unchanged.
Scope: how an audit event gets from a tool call to Postgres, what that costs, and what we do about it.

## What happens per call today

1. `gateway.Service.call` builds an `audit.Event` at every exit (denials, errors and successes alike)
   and calls `s.audit.Record(bg, e)` on the request path (`internal/gateway/gateway.go`, the `record`
   closure). `bg` carries the caller's values but not its cancellation, with a 5 second deadline
   (`bookkeepingTimeout`), so a cancelled request still leaves its event.
2. `s.audit` is `audit.Multi{audit.Slog, auditStore}` (`cmd/gateway/main.go`). Each call writes one
   structured log line, then the store.
3. With `--postgres-url` the store is `internal/audit/pgstore`. `Record` does one synchronous
   `INSERT INTO audit_events (...) VALUES (...)` and waits for Postgres to answer. It has its own
   2 second deadline (`recordTimeout`). Without Postgres the store is the in-memory `Ring`.
4. `Record` has no error return. A failed INSERT goes to `OnError`, which logs
   `audit store write failed` and nothing else. **An audit failure never fails or slows a call by more
   than 2 seconds, and the event is lost.** That is today's semantics, and any change must keep it:
   audit is best effort on the write side and never turns a successful call into an error.
5. Arguments are never stored, only their SHA-256 (`audit.HashArgs`). That is unchanged by this note.

So every call, including a read-only `get_city_state`, pays one Postgres round trip before the result
goes back to the agent.

## Cost

Request path. One network round trip and one commit per call. In the one hour soak (359,763 calls
at 100 per second) the full call p50 was 1.9 ms with the fake backend, so the INSERT is a large share
of it. A slow database adds directly to every call's latency, up to the 2 second cap, and ties up one
pool connection per in-flight call. The pool is shared with the confirmation, auth and manifest stores.

Postgres. Each INSERT is its own transaction: one WAL flush per call (`synchronous_commit` default),
four index entries (primary key, `(tenant, player, id)`, `(tenant, id)`, `(time)`) and heap growth.
In the same soak the Postgres container memory rose from 27 MiB to 154 MiB over the hour while the
gateway stayed between 11 and 19 MiB. Some of that is Postgres caching the growing table and indexes,
which is normal, but it is the part of the system that grows with audit volume.

## Table growth at 100 calls per second

These are estimates from the schema, not measurements. Check them with
`SELECT pg_size_pretty(pg_total_relation_size('audit_events'))` after a run.

| Quantity | Estimate |
|---|---|
| Rows per day | 8.64 million |
| Bytes per row, heap | about 150 (header, id, time, 64 character hash, short text fields) |
| Bytes per row, four indexes | about 150 |
| Growth per day | about 2.6 GB |
| Steady state at the 720 hour default retention | about 78 GB, plus bloat |

100 calls per second sustained is a heavy pilot. At 5 calls per second the same numbers are 20 times
smaller (about 4 GB at steady state), which is why the default has been fine so far.

## What the retention prune does

`auditPruneLoop` runs hourly and executes `DELETE FROM audit_events WHERE time < $1`. The `(time)` index
finds the rows, but one statement deletes about 360 thousand rows an hour at 100 per second (the
whole day's expired slice at once only when the process was down). It writes a delete record to WAL for
each row, maintains all four indexes, and leaves dead tuples for autovacuum. Heap and index bloat
follow until vacuum catches up, and the table never shrinks on disk without a rewrite. The prune also
takes row locks only on the deleted rows, so it does not block inserts, but it competes for I/O with them.

## Options

### (a) Keep it synchronous

Zero new code and the simplest failure model: the event is written before the result is returned, so
a crash after a call cannot lose it. Cost: every call pays the round trip and a commit, throughput is
bounded by Postgres commits, and a slow database slows every call.
Right for: pilots under about 20 calls per second.

### (b) Bounded in-process queue with a batch writer

`Record` puts the event on a bounded channel and returns. One goroutine collects events and writes
them in a batch (`pgx.CopyFrom`) when the batch is full or a flush interval passes.

- Request path: no database round trip, only a non-blocking channel send.
- Postgres: one transaction and one WAL flush per batch instead of per event. At 100 per second with a
  200 millisecond interval that is about 5 commits per second instead of 100.
- Overflow, when the queue is full: **drop and count** (default) keeps the call path non-blocking, which
  matches today's rule that audit never holds up a call. **Block** waits up to the call's own
  bookkeeping deadline, then drops. Dropping is the same loss as a failed INSERT today, but it is
  visible: a counter and a log line.
- A crash or kill loses the events still in the queue (at most the queue size, and in practice at most
  one interval of events). A graceful shutdown drains the queue first. This is the real cost of the
  option and the reason it is opt-in.
- One writer, not several. Events keep their order, ids follow queue order, and the activity feed's
  newest-first paging stays meaningful. With several writers two calls by the same player could commit
  out of order. One `CopyFrom` writer handles tens of thousands of rows per second, far above what a
  gateway replica produces.
- A failed batch is counted as dropped and reported to `OnError`, the same as a failed INSERT today. No
  retry, so memory stays bounded and behavior stays easy to reason about.
- Reads: the feed queries Postgres, so a just-queued event is not visible until it is written. The
  store therefore flushes before every `Query` (a barrier that waits for earlier events), which gives
  read-your-writes inside one process at the cost of one flush per feed request. Other replicas see an
  event after at most one flush interval.

### (c) Partitioning with retention by dropping partitions

Range partition `audit_events` by day. Retention becomes `DROP TABLE` of an old partition: instant, no
dead tuples, no bloat, space returned to the OS. Inserts and indexes stay small per partition.
Cost: a schema change that needs a migration story (the project has no migration framework), a job to
create future partitions, and queries across partitions for long feeds. `bigserial` ids stay global.
Right for: sustained high volume with long retention.

## Recommendation

Do (b) now, behind a flag, default off. It removes the request path cost and most of the write
amplification with a small change and keeps today's failure semantics. Keep (a) as the default until
a pilot shows the need, so nobody gets a window of possible loss they did not choose. Plan (c) as the
follow-up once retention of more than a few days at tens of calls per second is a real requirement,
because (b) does not fix prune bloat, only the write path.

## What was built

Flags (all on `cmd/gateway`; `--audit-queue` is ignored with a warning without `--postgres-url`):

| Flag | Default | Meaning |
|---|---|---|
| `--audit-queue` | 0 | Queue capacity in events. 0 keeps today's synchronous write |
| `--audit-batch-size` | 500 | Most events in one batch |
| `--audit-flush-interval` | 200ms | Longest an event waits before its batch is written |
| `--audit-overflow` | drop | `drop` or `block` when the queue is full |

Metrics: `audit_queue_depth` (gauge), `audit_dropped_total{reason}` (`overflow`, `write_error`,
`closed`), `audit_batch_size` and `audit_flush_duration_seconds` (histograms),
`audit_flush_errors_total`. Alert on any rate of `audit_dropped_total`.

Code: `audit.Batcher` (`internal/audit/batcher.go`) wraps any store that can write a batch.
`pgstore.Store.WriteBatch` writes with `CopyFrom`. `Query` and `Prune` flush the queue first. The
`auditest` contract suite runs against the Batcher over both the in-memory ring and Postgres.
Tests also cover batching by size and by interval, order, both overflow policies, a failed write,
Close draining, a blocked `Record` being woken by Close, concurrent records (every event is either
written or counted as dropped), and a gateway-level test that a failing audit store never fails a call.

## Measured

Run on a laptop with Docker Compose and the fake backend, so read it as a direction and not a benchmark.
Same stack, 32 workers as fast as they go for 40 seconds, 2000 players:

| | Synchronous | Batched (queue 10000, flush 2s) |
|---|---|---|
| Calls | 71,558 (1,789 per second) | 66,323 (1,658 per second) |
| Latency p50, p99 | 7.2 ms, 101.7 ms | 6.8 ms, 105.3 ms |
| Postgres commits during the run | 71,350 | 179 |

The commit count fell by a factor of about 400, which is the saving the batching was built for. Call
latency and throughput did not change beyond run to run noise (the two runs differ by about 8 percent
in throughput, and a later batched run reached 2,282 per second). At this load something other than
the audit INSERT limits the gateway: the same 32 errors appeared in both runs, and the load generator,
Redis and the backend share the machine. So the case for batching today is Postgres load and write
amplification, not request latency. A separate 20 second batched run wrote all 45,668 calls in 92
batches (about 496 events each) with nothing dropped, and a `SIGTERM` stop wrote the 20 events that
were still queued (70 of 70 rows).

## Not changed

- The default path, the 2 second INSERT deadline and `OnError` logging.
- The `Slog` line per call, which still happens synchronously on the request path. It is a buffered
  write to stderr and cheap, but it is also a copy of every event, so shipping logs is a second,
  independent audit trail.
- Retention and the prune. Option (c) is the fix for bloat and is not built.
