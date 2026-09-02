package transcribe

import "testing"

// The anchor is all-or-nothing on purpose: a partial base cannot map anything,
// and treating a missing tag as zero would place a caller's audio at a
// confidently wrong time rather than refusing to place it at all.
func TestSourceTimeBaseFromTags(t *testing.T) {
	cases := []struct {
		name                       string
		wallMS, timelineNS, rate   string
		wantKnown                  bool
		wantWallMS, wantTimelineNS int64
		wantRate                   uint32
	}{
		{
			name: "complete anchor", wallMS: "1756800000000", timelineNS: "1500000000", rate: "48000",
			wantKnown: true, wantWallMS: 1756800000000, wantTimelineNS: 1500000000, wantRate: 48000,
		},
		{
			name: "surrounding whitespace is tolerated", wallMS: " 1756800000000 ", timelineNS: " 0 ", rate: " 48000 ",
			wantKnown: true, wantWallMS: 1756800000000, wantTimelineNS: 0, wantRate: 48000,
		},
		{name: "no tags at all (recording predates the anchor)"},
		{name: "missing clock rate", wallMS: "1756800000000", timelineNS: "0"},
		{name: "missing wall clock", timelineNS: "0", rate: "48000"},
		{name: "zero clock rate", wallMS: "1756800000000", timelineNS: "0", rate: "0"},
		{name: "zero wall clock is not the epoch", wallMS: "0", timelineNS: "0", rate: "48000"},
		{name: "negative wall clock", wallMS: "-1", timelineNS: "0", rate: "48000"},
		{name: "unparseable wall clock", wallMS: "not-a-number", timelineNS: "0", rate: "48000"},
		{name: "clock rate overflows uint32", wallMS: "1756800000000", timelineNS: "0", rate: "4294967296"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sourceTimeBaseFromTags(tc.wallMS, tc.timelineNS, tc.rate)
			if got.Known != tc.wantKnown {
				t.Fatalf("Known = %v, want %v (got %+v)", got.Known, tc.wantKnown, got)
			}
			if !tc.wantKnown {
				if got != (SourceTimeBase{}) {
					t.Fatalf("an unusable anchor must be the zero value, got %+v", got)
				}
				return
			}
			if got.FirstPacketWallMS != tc.wantWallMS || got.FirstTimelineNS != tc.wantTimelineNS || got.ClockRate != tc.wantRate {
				t.Fatalf("got %+v, want wall=%d timeline=%d rate=%d", got, tc.wantWallMS, tc.wantTimelineNS, tc.wantRate)
			}
		})
	}
}

// A negative timeline position is legal: a track whose first packet predates
// the timeline origin sits before zero.
func TestSourceTimeBaseFromTagsAcceptsNegativeTimeline(t *testing.T) {
	got := sourceTimeBaseFromTags("1756800000000", "-250000000", "48000")
	if !got.Known || got.FirstTimelineNS != -250000000 {
		t.Fatalf("got %+v, want a known base at -250000000ns", got)
	}
}
