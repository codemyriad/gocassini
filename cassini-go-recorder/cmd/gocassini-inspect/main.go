package main

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"gocassini/internal/cassette"
)

type trackSummary struct {
	meta    cassette.TrackMetadata
	packets int
	ended   bool
	reason  string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <archive.csr>\n", os.Args[0])
		os.Exit(2)
	}
	path := os.Args[1]

	header, records, err := cassette.ReadAll(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read archive: %v\n", err)
		os.Exit(1)
	}

	tracks := map[uint32]*trackSummary{}
	var totalPackets int

	for _, rec := range records {
		switch rec.Type {
		case cassette.RecordTrackStart:
			if rec.TrackStart == nil {
				continue
			}
			tracks[rec.TrackStart.TrackRef] = &trackSummary{meta: *rec.TrackStart}
		case cassette.RecordRTPPacket:
			if rec.RTP == nil {
				continue
			}
			totalPackets++
			t := tracks[rec.RTP.TrackRef]
			if t == nil {
				continue
			}
			t.packets++
		case cassette.RecordTrackEnd:
			if rec.TrackEnd == nil {
				continue
			}
			t := tracks[rec.TrackEnd.TrackRef]
			if t == nil {
				continue
			}
			t.ended = true
			t.reason = rec.TrackEnd.Reason
		}
	}

	refs := make([]int, 0, len(tracks))
	for ref := range tracks {
		refs = append(refs, int(ref))
	}
	sort.Ints(refs)

	fmt.Printf("archive=%s version=%d created_at_ns=%d records=%d tracks=%d packets=%d\n", path, header.Version, header.CreatedAtUnixNano, len(records), len(tracks), totalPackets)
	for _, refInt := range refs {
		ref := uint32(refInt)
		t := tracks[ref]
		if t == nil {
			continue
		}
		participant := t.meta.ParticipantName
		if participant == "" {
			participant = "-"
		}
		reason := t.reason
		if !t.ended {
			reason = "(open)"
		}
		fmt.Printf(
			"track_ref=%d kind=%s codec=%s remote_session=%s participant=%s packets=%d end_reason=%s\n",
			ref,
			t.meta.Kind,
			t.meta.Codec,
			t.meta.RemoteSessionID,
			participant,
			t.packets,
			reason,
		)
	}

	if len(records) == 0 {
		exitErr(errors.New("archive contains zero records"))
	}
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
