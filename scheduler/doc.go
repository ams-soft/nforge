// Package scheduler is the nForge Cron scheduler: an independent infrastructure
// module (section scheduler, capability scheduler.Capability) that decides WHEN
// a handler runs. What the handler does, which period it covers and whether it
// is idempotent stay the application's responsibility.
//
// A domain module requires scheduler.Capability (and whatever its handlers use,
// such as database.Capability) and registers its jobs in Init, before anything
// starts:
//
//	s := module.Require(scope, scheduler.Capability)
//	err := s.AddJob("0 7 * * *", func(ctx context.Context) error { ... })
//
// The module needs no HTTP server, listener or broker. It is an ingress module:
// it starts after every module initialized and stops first, so a handler never
// runs against a database that is already closed.
//
// # Expressions
//
// Exactly five fields: minute, hour, day of month, month, day of week. Each
// accepts "*", a value, a range "a-b", a list "a,b" and a step "*/n" or
// "a-b/n"; months (JAN-DEC) and weekdays (SUN-SAT) may be named, and the
// weekday 7 is Sunday. Seconds and descriptors such as "@every" or "@daily" are
// rejected. When both day fields are restricted a day matches if either of them
// does (Vixie cron); with one of them "*" both must match. "0 7 * * *" is 07:00
// every day.
//
// # Time zone and DST
//
// Expressions are evaluated on the wall clock of scheduler.timezone (an IANA
// name, default UTC; the database's zone is never used). The tz database must
// be available: import time/tzdata in main for minimal containers. At a DST
// change:
//
//   - a wall time that does not exist (spring forward) fires once, shifted by
//     the length of the gap (02:30 runs at 03:30);
//   - a wall time that occurs twice (fall back) fires once, at its first
//     occurrence, for a fixed-time job (minute and hour restricted); a job with
//     a wildcard minute or hour fires on every matching instant of the
//     repeated hour.
//
// # Execution model
//
// One timer loop serves every job; there is no goroutine per job. Each firing
// starts the handler in its own goroutine with its own context, derived from
// the scheduler's lifecycle and canceled by Stop. Executions run in parallel:
// a slow run does not delay or cancel the next one. There is no queue, no
// backlog, no retry and no persistence. A firing missed because the process was
// down, suspended or saturated is NOT recovered later. An error returned by a
// handler is logged and does not stop the schedule; a panic is recovered and
// logged.
//
// scheduler.max_concurrency (default 0, unlimited) and WithMaxConcurrency cap
// the executions of ONE job running at once; when the cap is reached the firing
// is skipped (logged and counted), never queued. The cap is per job, so a slow
// job cannot block the others.
//
// # Replicas
//
// The mode is local: every instance runs every job. With several replicas a job
// runs once per replica, with no coordination and no exactly-once guarantee;
// make handlers idempotent or run a single scheduler instance. The distributed
// mode is reserved and unsupported: scheduler.mode: distributed fails the
// configuration, it never falls back to local. Job identifiers ("job-1", in
// registration order) are local to the process; a distributed mode would need
// identities that are stable across instances.
package scheduler
