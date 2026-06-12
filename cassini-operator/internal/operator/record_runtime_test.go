package operator

import (
	"errors"
	"testing"
)

func TestClassifyRecordStopReasonUnjoinableRoom(t *testing.T) {
	// The recorder tags definitive HTTP 4xx join rejections with
	// ErrUnjoinable and prints them on the "talk recorder stopping:" line
	// the operator scrapes into the stop detail. The literal marker string
	// "talk room unjoinable" is the contract between the two binaries
	// (D-374); this test pins it.
	logs := "talk recorder unjoinable: room check failed: talk room unjoinable: get room failed: code=404\n" +
		"talk recorder stopping: room check failed: talk room unjoinable: get room failed: code=404\n"
	detail := recordStopDetail(logs)
	if detail != "room check failed: talk room unjoinable: get room failed: code=404" {
		t.Fatalf("recordStopDetail() = %q", detail)
	}

	exitCode := 1
	got := classifyRecordStopReason(false, &exitCode, detail, errors.New("cassini record: exit status 1"))
	if got != "join_failed" {
		t.Fatalf("classifyRecordStopReason() = %q, want %q", got, "join_failed")
	}
}

func TestClassifyRecordStopReasonExistingCases(t *testing.T) {
	exitZero := 0
	exitOne := 1
	cases := []struct {
		name       string
		stopped    bool
		exitCode   *int
		stopDetail string
		runErr     error
		want       string
	}{
		{name: "operator stop", stopped: true, exitCode: &exitZero, stopDetail: "stop requested", want: "operator_requested"},
		{name: "room empty", exitCode: &exitZero, stopDetail: "room empty for 30s after remote participants left", want: "room_empty"},
		{name: "duration limit", exitCode: &exitZero, stopDetail: "duration limit reached", want: "duration_limit"},
		{name: "join call failed", exitCode: &exitOne, stopDetail: "join call failed: code=400", runErr: errors.New("cassini record: exit status 1"), want: "join_failed"},
		{name: "unclassified nonzero exit", exitCode: &exitOne, stopDetail: "something exploded", runErr: errors.New("cassini record: exit status 1"), want: "record_process_exit_nonzero"},
		{name: "clean exit no detail", exitCode: &exitZero, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRecordStopReason(tc.stopped, tc.exitCode, tc.stopDetail, tc.runErr); got != tc.want {
				t.Fatalf("classifyRecordStopReason() = %q, want %q", got, tc.want)
			}
		})
	}
}
