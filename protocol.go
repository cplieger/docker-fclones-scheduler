package main

// --- Trigger protocol (client <-> daemon, newline-delimited JSON) ---
//
// The `scan` subcommand is a thin client: it forwards its request over the
// daemon's unix socket and waits for the run result. Client and daemon ship
// in the same binary inside the same image, so there is no version skew to
// negotiate and the wire format carries no version field. The wire shape is
// the fleet's shared single-owner scheduler protocol (docker-renovate-
// scheduler and docker-rsync-scheduler speak the same frames).

// wireRequest is the single request line a client sends after connecting. A
// scan takes no arguments — the scan paths and action come from the daemon's
// environment config — so the request carries no fields; the empty JSON
// object is the frame that says "run one scan". Fields added here later must
// stay optional so an older client's `{}` keeps decoding.
type wireRequest struct{}

// wireEvent is one status line the daemon streams back. The client receives
// eventQueued on acceptance, eventStarted when the executor picks the request
// up (the gap between the two is queue wait behind an in-flight run), and
// exactly one eventDone as the final line.
type wireEvent struct {
	Event string `json:"event"`
	// Reason explains a not-OK outcome that isn't a plain run failure
	// (queue full, cancelled by shutdown), or annotates an OK outcome that
	// did not scan (the cross-container lock skip).
	Reason string `json:"reason,omitempty"`
	// DurationMs is the run's execution time on eventDone (0 when the
	// request never ran, e.g. cancelled or rejected).
	DurationMs int64 `json:"duration_ms,omitempty"`
	// OK is meaningful only on eventDone: the run outcome (never omitted,
	// so a failed run is explicit on the wire).
	OK bool `json:"ok"`
}

const (
	eventQueued  = "queued"
	eventStarted = "started"
	eventDone    = "done"
)
