// Package loadgen is the built-in RPS-focused engine.
//
// Open loop. The pacer decides when each request is due (its scheduled_at);
// responses never delay the next request. Each hit gets its own goroutine,
// which waits for a slot under a hard in-flight cap and then sends. The
// alternative, a fixed worker pool fed by a queue, was rejected: when the pool
// is exhausted the dispatcher either blocks (closed loop: the schedule slips
// and the lag is hidden) or queues behind a channel whose depth is another
// arbitrary cap. A goroutine per hit keeps the dispatcher from ever blocking,
// and a request held back by the cap is still sent, late, with the delay in its
// send_lag.
//
// Shedding. The number of goroutines is bounded: at most max_in_flight
// requests are sent at once and at most max_waiting (default: the same number)
// wait for a slot. A due hit that finds both full is not sent; it is counted in
// Stats().Shed and logged once. Shedding means the client is the bottleneck, or
// the target is so far behind that the client cannot keep the offered rate
// honestly. The stage then fails the achieved-versus-offered check and
// client_bound and is marked invalid, which is the honest
// outcome; the alternative, a goroutine per hit without limit, would exhaust the
// client's memory against a dead target.
//
// Times. Every instant comes from one clock (Options.Now, time.Now by
// default), whose readings carry Go's monotonic component, so differences are
// immune to clock steps. Scheduling, sleeping and send lag use those readings
// in every phase (each phase starts from the previous phase's monotonic end);
// they are converted to UTC only for the returned windows and the rows.
//
// sent_at is the reading taken immediately before the request is handed to the
// HTTP transport, not httptrace's WroteRequest. WroteRequest comes after
// dialing and the TLS handshake, so a new connection would be booked as send
// lag, and send lag must mean only client-side scheduling delay for the
// client_bound check. The connection setup is part of what a
// user waits for and stays inside TTFB and e2e.
//
// Rows. A finished request releases its in-flight slot, then queues its row for
// one writer goroutine, so a slow sink never holds back sending. A full queue
// blocks only the finishing request and is counted (Stats().RowQueueBlocks). At
// the end of the run the sink gets the drain time plus a short grace; a sink
// still stuck then is reported as an error and the rows it never took are
// counted in Stats().Lost, instead of hanging the run.
//
// Cancellation. Cancelling the context stops pacing at once. Requests already
// sent get the drain time to finish; those still running when it ends are cut
// off, and each is recorded (raw first) with status 0 and error class
// "abandoned", including a stream cancelled midway. Due requests that were never
// sent are only counted (Stats().Unsent).
//
// Proxies. The transport ignores HTTP_PROXY, HTTPS_PROXY and NO_PROXY on
// purpose: the target hop is addressed directly, because a proxy in the path
// would add its own latency to every measurement.
package loadgen
