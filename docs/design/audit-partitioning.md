# Design note: audit table partitioning and retention

Status: proposed and deferred. No baseline soak has been run, so there is no measured need yet. Nothing here is built. The default (one table, hourly `DELETE`) stays until this lands.
Scope: how the `audit_events` table is kept small on a long-running pilot.

## The problem

Every call writes one row (`internal/audit/pgstore`). Retention is a loop that runs
`DELETE FROM audit_events WHERE time < $1` once an hour (`auditPruneLoop`, default 30 days).

At the soak rate of 100 calls per second that is 8.6 million rows a day and about 260 million rows
in the 30 day window. A plain `DELETE` of one hour of rows (360,000) has these costs:

- Deleted rows stay as dead tuples until autovacuum visits them. The table and its three indexes
  stay at their peak size, and the heap rarely shrinks without `VACUUM FULL`.
- The delete and the vacuum that follows compete with the insert path for I/O.
- Index entries for deleted rows are removed only by vacuum, so the `(tenant, id DESC)` indexes bloat.

None of this has been measured yet. The 1 hour soak was too short to show it, and the first step of
this note is to measure it (see "Plan").

## Proposal

Partition `audit_events` by `RANGE (time)`, one partition per day. Retention becomes
`DROP TABLE` on whole partitions: instant, no dead tuples, no vacuum, space returned to the
operating system.

```sql
CREATE TABLE audit_events (
  id bigint GENERATED ALWAYS AS IDENTITY,
  tenant text NOT NULL, player text NOT NULL, tool text NOT NULL, tier text NOT NULL,
  outcome text NOT NULL, code text NOT NULL, args_sha text NOT NULL,
  time timestamptz NOT NULL,
  PRIMARY KEY (time, id)
) PARTITION BY RANGE (time);
CREATE INDEX ON audit_events (tenant, player, id DESC);
CREATE INDEX ON audit_events (tenant, id DESC);
```

Things that change, and the decisions:

1. **Primary key.** A unique index on a partitioned table must include the partition key, so the key
   becomes `(time, id)`. `id` stays unique in practice because it comes from one sequence, and
   nothing relies on a database constraint for that.
2. **The activity feed keeps its `before` cursor on `id`.** Feed queries filter by tenant and order by
   `id DESC LIMIT n`. Postgres plans that as a merge of per-partition index scans, so a query touches
   only as many partitions as it needs to fill the page. The `since` filter adds a `time` bound, which
   prunes partitions outright. No API change.
3. **Creating partitions.** The gateway creates today's and the next three days' partitions in the
   same hourly loop that prunes. It takes `pg_try_advisory_lock` so that only one replica does it. A
   `DEFAULT` partition catches rows if the loop has been down for longer than the look-ahead, so an
   insert never fails because a partition is missing. A non-empty default partition raises an
   alert.
4. **Retention.** The loop drops partitions whose upper bound is older than `--audit-retention`.
   Retention granularity becomes one day: events live up to a day longer than configured, never
   shorter.
5. **Migration.** Opt-in with `--audit-partition` (off by default). On first start with the flag the
   gateway checks `pg_class.relkind` of `audit_events`:
   - missing: create the partitioned table;
   - `r` (the current plain table): rename it to `audit_events_legacy`, create the partitioned table,
     and read from both (a `UNION ALL` in `Query`) until the legacy table is older than the
     retention, then drop it. The `Query` change is small because both have the same columns;
   - `p`: nothing to do.

   There is no in-place conversion and no long lock: the rename is instant, and old rows are never
   copied. The cost is a two-table read for up to one retention period.
6. **Batching writer.** Unchanged. `COPY` or multi-row `INSERT` into a partitioned table routes each
   row, which costs a little CPU per row. To be measured in the same soak.

## What this does not do

- It does not change what is stored. Arguments are still never stored, only their hash.
- It does not give per-tenant retention. All tenants share the one window. That would need
  partitioning by tenant as well and is not worth the complexity before a second tenant exists.
- It does not export events elsewhere. For an immutable archive, ship the slog lines to a log store.

## Plan

1. **Measure the baseline.** A 6 hour soak at 100 per second with `--audit-retention=1h`, so
   deletes start early: table and index size, vacuum time, p99 during the prune, and insert latency.
   Decide from the numbers whether the work is worth doing. If the plain table stays flat, close this
   note as "not needed yet".
2. Build `--audit-partition` behind the flag with a test for each state of the migration
   (missing, plain, partitioned) and for the missing-partition fallback.
3. Repeat the soak with the flag and compare. Add the result to the runbook.

## Open questions

- Daily or hourly partitions? Daily is easy to reason about. Hourly keeps `DROP` granularity close
  to the setting, but makes 720 partitions on a 30 day window, which slows planning. Start with daily.
- Should `--audit-retention` accept values under one day once partitioned? Probably not: it would
  silently round up.
