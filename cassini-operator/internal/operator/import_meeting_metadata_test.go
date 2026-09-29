package operator

import (
	"encoding/json"
	"testing"
)

func TestMapSeedMetadataUsesDestinationIDsAndPreservesFields(t *testing.T) {
	raw := json.RawMessage(`{"id":"meeting","audioPath":"./meetings/daily--12:30.opus","title":"Daily","roomName":"Team","extension":{"x":1}}`)
	catalog := siteCatalog{Version: "cassini.viewer.catalog.v1", Meetings: []json.RawMessage{raw}}
	rows, err := mapSeedMetadata(catalog, map[int64]string{987: "daily--12:30.opus"})
	if err != nil || len(rows) != 1 || rows[0].id != 987 || string(rows[0].entry) != string(raw) {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if _, err := mapSeedMetadata(catalog, map[int64]string{123: "other.opus"}); err == nil {
		t.Fatal("accepted absent recording")
	}
	catalog.Meetings = append(catalog.Meetings, raw)
	if _, err := mapSeedMetadata(catalog, map[int64]string{987: "daily--12:30.opus"}); err == nil {
		t.Fatal("accepted duplicate")
	}
}
