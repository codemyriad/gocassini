package remux

import "testing"

func TestPlanMergeOrdersByRecvNSAndComputesOffsets(t *testing.T) {
	inputs := []StreamInput{
		{
			StreamID:    "s_2",
			FirstRecvNS: 5_000_000_000,
			SourceStart: 0.25,
		},
		{
			StreamID:    "s_1",
			FirstRecvNS: 2_000_000_000,
			SourceStart: 0,
		},
		{
			StreamID:    "s_3",
			FirstRecvNS: 5_000_000_000,
			SourceStart: 0.10,
		},
	}

	got := PlanMerge(inputs)
	if len(got) != 3 {
		t.Fatalf("planned len: got=%d want=3", len(got))
	}
	if got[0].StreamID != "s_1" {
		t.Fatalf("first stream: got=%s want=s_1", got[0].StreamID)
	}
	if got[0].OffsetSeconds != 0 {
		t.Fatalf("first offset: got=%f want=0", got[0].OffsetSeconds)
	}

	if got[1].StreamID != "s_2" {
		t.Fatalf("second stream order: got=%s want=s_2", got[1].StreamID)
	}
	if got[2].StreamID != "s_3" {
		t.Fatalf("third stream order: got=%s want=s_3", got[2].StreamID)
	}

	wantSecond := 3.0 - 0.25
	if diff := got[1].OffsetSeconds - wantSecond; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("second offset: got=%f want=%f", got[1].OffsetSeconds, wantSecond)
	}
	wantThird := 3.0 - 0.10
	if diff := got[2].OffsetSeconds - wantThird; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("third offset: got=%f want=%f", got[2].OffsetSeconds, wantThird)
	}
}

func TestKindFromCodec(t *testing.T) {
	if got := KindFromCodec("audio/opus"); got != "audio" {
		t.Fatalf("audio codec kind mismatch: %s", got)
	}
	if got := KindFromCodec("video/VP8"); got != "video" {
		t.Fatalf("video codec kind mismatch: %s", got)
	}
	if got := KindFromCodec("application/binary"); got != "unknown" {
		t.Fatalf("unknown codec kind mismatch: %s", got)
	}
}

func TestElementaryExtension(t *testing.T) {
	if got := ElementaryExtension("audio/opus"); got != ".ogg" {
		t.Fatalf("opus extension mismatch: %s", got)
	}
	if got := ElementaryExtension("video/vp8"); got != ".ivf" {
		t.Fatalf("vp8 extension mismatch: %s", got)
	}
	if got := ElementaryExtension("video/h264"); got != "" {
		t.Fatalf("unexpected extension for unsupported codec: %s", got)
	}
}
