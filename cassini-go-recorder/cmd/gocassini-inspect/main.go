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
)

type trackSummary struct {
	meta    cassette.TrackMetadata
	packets int
	ended   bool
	reason  string
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

	for _, stream := range sess.PacketStreams {
		path := filepath.Join(streamsDir, stream.StreamID+".rtplog")
		summary, err := inspectStreamLog(path)
		if err != nil {
			fmt.Printf("stream=%s error=%v\n", stream.StreamID, err)
			continue
		}
		fmt.Printf(
			"stream=%s ltid=%s kind=%s codec=%s rtp=%d rtcp=%d first_ns=%d last_ns=%d\n",
			stream.StreamID,
			stream.LTID,
			stream.Codec,
			stream.MID,
			summary.rtp,
			summary.rtcp,
			summary.first,
			summary.last,
		)

		if idxInfo, err := os.Stat(path + ".idx"); err == nil {
			fmt.Printf("  index=%s bytes=%d\n", path+".idx", idxInfo.Size())
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
