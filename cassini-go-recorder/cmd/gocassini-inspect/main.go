package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"gocassini/internal/cassette"
	"gocassini/pkg/core/session"
	"gocassini/pkg/core/store"
	"gocassini/pkg/core/validate"
)

type trackSummary struct {
	meta    cassette.TrackMetadata
	packets int
	ended   bool
	reason  string
}

type inspectedSessionStream struct {
	packet  session.PacketStream
	summary streamSummary
	kind    string
}

type segmentChurnSummary struct {
	Segments    int
	SSRCChanges int
	PTChanges   int
	MaxGapNS    uint64
	FirstNS     uint64
	LastNS      uint64
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <archive.csr|session.json|session-dir>\n", os.Args[0])
		os.Exit(2)
	}
	path := os.Args[1]

	if artifactPath, ok := detectSessionArtifactPath(path); ok {
		if err := inspectSessionArtifact(artifactPath); err != nil {
			exitErr(fmt.Errorf("read session artifact: %w", err))
		}
		return
	}

	header, records, err := cassette.ReadAll(path)
	if err != nil {
		exitErr(fmt.Errorf("read archive: %w", err))
	}
	inspectArchive(path, header, records)
}

func inspectArchive(source string, header cassette.Header, records []cassette.Record) {
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

	fmt.Printf(
		"archive=%s version=%d created_at_ns=%d records=%d tracks=%d packets=%d\n",
		source,
		header.Version,
		header.CreatedAtUnixNano,
		len(records),
		len(tracks),
		totalPackets,
	)
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

func inspectSessionArtifact(sessionPath string) error {
	sessionJSON := sessionPath
	raw, err := os.ReadFile(sessionJSON)
	if err != nil {
		return fmt.Errorf("read session json: %w", err)
	}

	var sess session.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return fmt.Errorf("unmarshal session json: %w", err)
	}

	baseDir := filepath.Dir(sessionJSON)
	streamsDir := filepath.Join(baseDir, "streams")
	eventsPath := filepath.Join(baseDir, sess.EventsSourcePath)
	eventsSize := int64(0)
	eventCount := 0
	if f, err := os.Open(eventsPath); err == nil {
		eventsSize = mustFileSize(f)
		eventCount, _ = countLines(f)
		_ = f.Close()
	}

	fmt.Printf("session=%s room=%s started=%s streams=%d logical_tracks=%d participants=%d\n",
		sess.SessionID,
		sess.Platform.Room,
		sess.StartedWallUTC,
		len(sess.PacketStreams),
		len(sess.LogicalTracks),
		len(sess.Participants),
	)
	fmt.Printf("session_json=%s events=%s events_bytes=%d events_lines=%d streams_dir=%s\n",
		sessionJSON,
		eventsPath,
		eventsSize,
		eventCount,
		streamsDir,
	)

	logicalByLTID := make(map[string]session.LogicalTrack, len(sess.LogicalTracks))
	for _, logical := range sess.LogicalTracks {
		logicalByLTID[logical.LTID] = logical
	}

	inspected := make([]inspectedSessionStream, 0, len(sess.PacketStreams))
	for _, stream := range sess.PacketStreams {
		path := filepath.Join(streamsDir, stream.StreamID+".rtplog")
		summary, err := inspectStreamLog(path)
		if err != nil {
			fmt.Printf("stream=%s error=%v\n", stream.StreamID, err)
			continue
		}
		validation, validationErr := validate.CheckLog(path)
		issueCount := 0
		if validationErr == nil {
			issueCount = validation.IssueCount
		}
		kind := "-"
		if logical, ok := logicalByLTID[stream.LTID]; ok && logical.Kind != "" {
			kind = logical.Kind
		}
		durationNS := uint64(0)
		if summary.last > summary.first {
			durationNS = summary.last - summary.first
		}
		fmt.Printf(
			"stream=%s ltid=%s kind=%s mid=%s rid=%s codec=%s ssrc=%d pt=%d rtp=%d rtcp=%d first_ns=%d last_ns=%d dur_ms=%.3f issues=%d\n",
			stream.StreamID,
			stream.LTID,
			kind,
			stream.MID,
			stream.RID,
			stream.Codec,
			stream.PrimarySSRC,
			stream.PT,
			summary.rtp,
			summary.rtcp,
			summary.first,
			summary.last,
			float64(durationNS)/1e6,
			issueCount,
		)
		if validationErr != nil {
			fmt.Printf("  validation_error=%v\n", validationErr)
		} else if validation.IssueCount > 0 {
			limit := len(validation.Issues)
			if limit > 3 {
				limit = 3
			}
			for idx := 0; idx < limit; idx++ {
				issue := validation.Issues[idx]
				fmt.Printf("  issue[%d]=%s recv_ns=%d %s\n", idx, issue.Code, issue.RecvMonoNS, issue.Message)
			}
		}
		inspected = append(inspected, inspectedSessionStream{
			packet:  stream,
			summary: summary,
			kind:    kind,
		})

		if idxInfo, err := os.Stat(path + ".idx"); err == nil {
			fmt.Printf("  index=%s bytes=%d\n", path+".idx", idxInfo.Size())
		}
	}

	if len(inspected) > 0 {
		fmt.Println("segment_churn:")
		printSegmentChurn(inspected, logicalByLTID)
	}

	if reasons, err := streamCloseReasons(eventsPath); err == nil && len(reasons) > 0 {
		fmt.Println("stream_close_reasons:")
		keys := make([]string, 0, len(reasons))
		for reason := range reasons {
			keys = append(keys, reason)
		}
		sort.Strings(keys)
		for _, reason := range keys {
			fmt.Printf("  reason=%s count=%d\n", reason, reasons[reason])
		}
	}
	return nil
}

type streamSummary struct {
	rtp   int
	rtcp  int
	first uint64
	last  uint64
}

func printSegmentChurn(streams []inspectedSessionStream, logicalByLTID map[string]session.LogicalTrack) {
	grouped := make(map[string][]inspectedSessionStream)
	for _, stream := range streams {
		grouped[stream.packet.LTID] = append(grouped[stream.packet.LTID], stream)
	}

	ltids := make([]string, 0, len(grouped))
	for ltid := range grouped {
		ltids = append(ltids, ltid)
	}
	sort.Strings(ltids)

	for _, ltid := range ltids {
		entries := grouped[ltid]
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].packet.StartMonoNS == entries[j].packet.StartMonoNS {
				return entries[i].packet.StreamID < entries[j].packet.StreamID
			}
			return entries[i].packet.StartMonoNS < entries[j].packet.StartMonoNS
		})

		stats := summarizeSegmentChurn(entries)
		participant := "-"
		source := "-"
		if logical, ok := logicalByLTID[ltid]; ok {
			if logical.ParticipantID != "" {
				participant = logical.ParticipantID
			}
			if logical.Source != "" {
				source = logical.Source
			}
		}
		fmt.Printf(
			"  ltid=%s participant=%s source=%s segments=%d ssrc_changes=%d pt_changes=%d max_gap_ms=%.3f first_ns=%d last_ns=%d\n",
			ltid,
			participant,
			source,
			stats.Segments,
			stats.SSRCChanges,
			stats.PTChanges,
			float64(stats.MaxGapNS)/1e6,
			stats.FirstNS,
			stats.LastNS,
		)
	}
}

func summarizeSegmentChurn(entries []inspectedSessionStream) segmentChurnSummary {
	if len(entries) == 0 {
		return segmentChurnSummary{}
	}
	stats := segmentChurnSummary{
		Segments: len(entries),
		FirstNS:  entries[0].summary.first,
		LastNS:   entries[0].summary.last,
	}
	for idx := 1; idx < len(entries); idx++ {
		prev := entries[idx-1]
		curr := entries[idx]
		if prev.packet.PrimarySSRC != curr.packet.PrimarySSRC {
			stats.SSRCChanges++
		}
		if prev.packet.PT != curr.packet.PT {
			stats.PTChanges++
		}
		if curr.summary.first > prev.summary.last {
			gap := curr.summary.first - prev.summary.last
			if gap > stats.MaxGapNS {
				stats.MaxGapNS = gap
			}
		}
		if curr.summary.first < stats.FirstNS {
			stats.FirstNS = curr.summary.first
		}
		if curr.summary.last > stats.LastNS {
			stats.LastNS = curr.summary.last
		}
	}
	return stats
}

func inspectStreamLog(path string) (streamSummary, error) {
	rdr, err := store.OpenReader(path)
	if err != nil {
		return streamSummary{}, err
	}
	defer func() {
		_ = rdr.Close()
	}()

	var summary streamSummary
	for {
		rec, err := rdr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return streamSummary{}, err
		}
		switch rec.Kind {
		case store.KindRTP:
			summary.rtp++
		case store.KindRTCP:
			summary.rtcp++
		}
		if summary.first == 0 || rec.RecvMonoNS < summary.first {
			summary.first = rec.RecvMonoNS
		}
		if rec.RecvMonoNS > summary.last {
			summary.last = rec.RecvMonoNS
		}
	}
	return summary, nil
}

func detectSessionArtifactPath(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if info.IsDir() {
		sessionPath := filepath.Join(path, "session.json")
		if _, err := os.Stat(sessionPath); err == nil {
			return sessionPath, true
		}
		return "", false
	}
	base := filepath.Base(path)
	if base == "session.json" {
		return path, true
	}
	return "", false
}

func countLines(reader io.Reader) (int, error) {
	scanner := bufio.NewScanner(reader)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	return lines, scanner.Err()
}

func streamCloseReasons(eventsPath string) (map[string]int, error) {
	file, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	reasons := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		if eventType != "stream_closed" {
			continue
		}
		reason, _ := event["reason"].(string)
		if reason == "" {
			reason = "(missing)"
		}
		reasons[reason]++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return reasons, nil
}

func mustFileSize(file *os.File) int64 {
	if info, err := file.Stat(); err == nil {
		return info.Size()
	}
	return 0
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
