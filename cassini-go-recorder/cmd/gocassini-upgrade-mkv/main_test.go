package main

import "testing"

func TestDeriveDefaultOutputPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "/tmp/meeting.mkv", want: "/tmp/meeting.v1.mkv"},
		{input: "/tmp/MEETING.MKV", want: "/tmp/MEETING.v1.mkv"},
		{input: "/tmp/meeting", want: "/tmp/meeting.v1.mkv"},
	}

	for _, tc := range tests {
		if got := deriveDefaultOutputPath(tc.input); got != tc.want {
			t.Fatalf("deriveDefaultOutputPath(%q)=%q want=%q", tc.input, got, tc.want)
		}
	}
}
