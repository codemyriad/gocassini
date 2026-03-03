package talk

import "testing"

func TestStreamSegmentRotationReason(t *testing.T) {
	tests := []struct {
		name     string
		prevSSRC uint32
		nextSSRC uint32
		prevPT   uint8
		nextPT   uint8
		want     string
	}{
		{
			name:     "ssrc only",
			prevSSRC: 100,
			nextSSRC: 101,
			prevPT:   111,
			nextPT:   111,
			want:     "segment-rotate:ssrc:100->101",
		},
		{
			name:     "payload type only",
			prevSSRC: 100,
			nextSSRC: 100,
			prevPT:   111,
			nextPT:   109,
			want:     "segment-rotate:pt:111->109",
		},
		{
			name:     "ssrc and payload type",
			prevSSRC: 100,
			nextSSRC: 200,
			prevPT:   111,
			nextPT:   109,
			want:     "segment-rotate:ssrc:100->200,pt:111->109",
		},
		{
			name:     "no change",
			prevSSRC: 100,
			nextSSRC: 100,
			prevPT:   111,
			nextPT:   111,
			want:     "segment-rotate:unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := streamSegmentRotationReason(tc.prevSSRC, tc.nextSSRC, tc.prevPT, tc.nextPT)
			if got != tc.want {
				t.Fatalf("unexpected reason: got=%q want=%q", got, tc.want)
			}
		})
	}
}
