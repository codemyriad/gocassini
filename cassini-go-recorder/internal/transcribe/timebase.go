package transcribe

import "time"

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

// recordedAtUTCFromStreams returns the wall-clock instant of playback position
// 0 of the meeting timeline, as RFC 3339 UTC with millisecond precision, or ""
// when no audio stream carries a wall-clock anchor (MKVs remuxed before
// 7ba661f9, 2026-09-02).
//
// Which instant position 0 is, on the Talk path:
//
//   - The remux puts every stream (audio and video) on one timeline whose
//     origin is the EARLIEST stream's first received packet. PlanMerge takes
//     base = the smallest FirstTimelineNS (pkg/core/remux/plan.go:44) and
//     shifts each stream to desired = FirstTimelineNS - base (plan.go:47-50),
//     applied with -copyts/-itsoffset (pkg/core/remux/artifact.go:360-363).
//     FirstTimelineNS is the stream's first receive time on the session clock
//     (artifact.go:289, 299, 321).
//   - The build keeps that origin. Each track is decoded with -copyts and its
//     initial offset restored as silence (sparseTimelineDecodeArgs,
//     internal/transcribe/audio.go:650-677), and the tracks are amixed from 0
//     (MixDownToWebM, audio.go:522). So MKV PTS t, mix position t and word
//     startMs t are the same point, and the packed .opus carries that mix.
//   - session_started_at (metadata.go:170, the session's StartedWallUTC from
//     internal/talk/session_artifact.go:135) is NOT position 0. It is when the
//     recorder joined, before any media arrived, typically seconds earlier;
//     using it would shift every word's wall-clock time by that gap.
//   - Each stream's first_packet_wall_ms is StartedWallUTC plus the session
//     clock delta to that stream's first packet (firstPacketWallMS,
//     artifact.go:442-449, tagged at metadata.go:228). That packet sits at
//     FirstPacketTimeMS, its first packet PTS, on the MKV timeline.
//
// Therefore position 0 = first_packet_wall_ms - FirstPacketTimeMS for any
// anchored stream. The earliest anchored audio stream is used, being the one
// closest to the origin.
func recordedAtUTCFromStreams(streams []AudioStream) string {
	found := false
	var best AudioStream
	for _, stream := range streams {
		if !stream.TimeBase.Known {
			continue
		}
		if !found || stream.FirstPacketTimeMS < best.FirstPacketTimeMS {
			best, found = stream, true
		}
	}
	if !found {
		return ""
	}
	originMS := best.TimeBase.FirstPacketWallMS - best.FirstPacketTimeMS
	return time.UnixMilli(originMS).UTC().Format(recordedAtUTCLayout)
}

// recordedAtUTCLayout writes millisecond precision and always ends in Z when
// given a UTC time, matching the format's recordedAtUtc pattern.
const recordedAtUTCLayout = "2006-01-02T15:04:05.000Z07:00"
