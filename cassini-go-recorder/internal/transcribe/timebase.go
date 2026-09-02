package transcribe

// SourceTimeBase is a recorded track's wall-clock anchor, read from the MKV
// stream tags the remux writes (pkg/core/remux/metadata.go).
//
// It exists so that an instant expressed in recorder-host wall time can be
// converted to a position on the meeting timeline exactly: both sides derive
// from the same monotonic clock, so no estimation is involved on the
// recorder's side.
type SourceTimeBase struct {
	// FirstPacketWallMS is when this track's first packet arrived, in Unix
	// milliseconds, and FirstTimelineNS is where that instant sits on the
	// meeting timeline. Together they map recorder wall time to meeting time.
	FirstPacketWallMS int64
	FirstTimelineNS   int64
	// ClockRate is the track's RTP clock rate. Janus does not change the rate
	// it relays, only the offset, so this stays correct for the sender's clock
	// even though the timestamps themselves do not.
	ClockRate uint32
	// Known distinguishes an anchor that was read from one that was absent.
	// Recordings made before the remux emitted these tags have Known=false,
	// and callers must treat that as "unknown" rather than as the epoch.
	Known bool
}
