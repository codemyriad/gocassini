package remux

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gocassini/pkg/core/depacket"
	"gocassini/pkg/core/session"
)

type BuildOptions struct {
	WorkDir      string
	KeepWork     bool
	StrictCodecs bool
	Title        string
}

type BuildResult struct {
	SessionJSONPath string
	OutputPath      string
	WorkDir         string
	Segments        int
	StreamPlans     []StreamPlan
}

type StreamPlan struct {
	StreamID               string  `json:"stream_id"`
	LTID                   string  `json:"ltid"`
	Kind                   string  `json:"kind"`
	Codec                  string  `json:"codec"`
	ParticipantID          string  `json:"participant_id,omitempty"`
	ParticipantName        string  `json:"participant_name,omitempty"`
	MID                    string  `json:"mid,omitempty"`
	RID                    string  `json:"rid,omitempty"`
	ClockRate              uint32  `json:"clock_rate,omitempty"`
	PT                     uint8   `json:"pt,omitempty"`
	RTPPackets             int     `json:"rtp_packets"`
	FirstRecvNS            uint64  `json:"first_recv_ns"`
	FirstTimelineNS        int64   `json:"first_timeline_ns"`
	TimelineAdjustNS       int64   `json:"timeline_adjust_ns"`
	TimelineSamples        int     `json:"timeline_samples"`
	TimelineRawDurationNS  int64   `json:"timeline_raw_duration_ns"`
	TimelineCorrDurationNS int64   `json:"timeline_corrected_duration_ns"`
	SourceStartSeconds     float64 `json:"source_start_seconds"`
	OffsetSeconds          float64 `json:"offset_seconds"`
}

type segmentArtifact struct {
	Stream           session.PacketStream
	Kind             string
	TempPath         string
	FirstNS          uint64
	FirstTimelineNS  int64
	TimelineAdjustNS int64
	TimelineProfile  timelineProfile
	SourceStart      float64
	Packets          int
}

func BuildFromSession(inputPath, outputPath string, opts BuildOptions) (BuildResult, error) {
	sessionJSON, err := ResolveSessionJSONPath(inputPath)
	if err != nil {
		return BuildResult{}, err
	}
	sess, err := loadSession(sessionJSON)
	if err != nil {
		return BuildResult{}, err
	}

	workDir, cleanup, err := prepareWorkDir(opts.WorkDir, opts.KeepWork)
	if err != nil {
		return BuildResult{}, err
	}
	defer cleanup()

	segments, err := buildSegmentMKVs(sessionJSON, sess, workDir, opts.StrictCodecs)
	if err != nil {
		return BuildResult{}, err
	}
	if len(segments) == 0 {
		return BuildResult{}, errors.New("no remuxable streams found in session artifact")
	}

	planned := planSegments(segments)
	streamPlans := buildStreamPlans(sess, segments, planned)
	if err := mergeSegments(outputPath, opts.Title, sess, segments, streamPlans, workDir); err != nil {
		return BuildResult{}, err
	}

	return BuildResult{
		SessionJSONPath: sessionJSON,
		OutputPath:      outputPath,
		WorkDir:         workDir,
		Segments:        len(segments),
		StreamPlans:     streamPlans,
	}, nil
}

func ResolveSessionJSONPath(input string) (string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("stat session path: %w", err)
	}
	if info.IsDir() {
		path := filepath.Join(input, "session.json")
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("session.json not found in %s", input)
		}
		return path, nil
	}
	if filepath.Base(input) == "session.json" {
		return input, nil
	}
	return "", fmt.Errorf("expected session dir or session.json, got: %s", input)
}

func loadSession(path string) (session.Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return session.Session{}, fmt.Errorf("read session json: %w", err)
	}
	var sess session.Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return session.Session{}, fmt.Errorf("parse session json: %w", err)
	}
	return sess, nil
}

func prepareWorkDir(configured string, keep bool) (string, func(), error) {
	if configured != "" {
		if err := os.MkdirAll(configured, 0o755); err != nil {
			return "", nil, fmt.Errorf("create work dir: %w", err)
		}
		return configured, func() {}, nil
	}

	tempDir, err := os.MkdirTemp("", "gocassini-remux-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp work dir: %w", err)
	}
	cleanup := func() {
		if keep {
			return
		}
		_ = os.RemoveAll(tempDir)
	}
	return tempDir, cleanup, nil
}

func buildSegmentMKVs(
	sessionJSON string,
	sess session.Session,
	workDir string,
	strictCodecs bool,
) ([]segmentArtifact, error) {
	baseDir := filepath.Dir(sessionJSON)
	streamsDir := filepath.Join(baseDir, "streams")

	logicalByLTID := map[string]session.LogicalTrack{}
	for _, logical := range sess.LogicalTracks {
		logicalByLTID[logical.LTID] = logical
	}

	out := make([]segmentArtifact, 0, len(sess.PacketStreams))
	for _, stream := range sess.PacketStreams {
		codec := strings.ToLower(strings.TrimSpace(stream.Codec))
		kind := logicalByLTID[stream.LTID].Kind
		if kind == "" {
			kind = KindFromCodec(codec)
		}
		ext := ElementaryExtension(codec)
		if ext == "" {
			if strictCodecs {
				return nil, fmt.Errorf("unsupported codec for stream %s: %s", stream.StreamID, stream.Codec)
			}
			continue
		}

		logPath := filepath.Join(streamsDir, stream.StreamID+".rtplog")
		if _, err := os.Stat(logPath); err != nil {
			if strictCodecs {
				return nil, fmt.Errorf("stream log missing for %s: %w", stream.StreamID, err)
			}
			continue
		}

		elementaryPath := filepath.Join(workDir, stream.StreamID+ext)
		writeResult, err := depacket.WriteElementaryFromRTPLog(logPath, codec, stream.ClockRate, elementaryPath)
		if err != nil {
			if strictCodecs {
				return nil, fmt.Errorf("depacketize stream %s: %w", stream.StreamID, err)
			}
			continue
		}
		if writeResult.RTPPackets == 0 {
			continue
		}

		segmentPath := filepath.Join(workDir, stream.StreamID+".mkv")
		if err := composeSingleTrackMKV(elementaryPath, segmentPath, kind); err != nil {
			if strictCodecs {
				return nil, fmt.Errorf("compose stream %s mkv: %w", stream.StreamID, err)
			}
			continue
		}
		if !probeHasUsableStream(segmentPath) {
			if strictCodecs {
				return nil, fmt.Errorf("compose stream %s mkv: unusable segment output", stream.StreamID)
			}
			continue
		}

		out = append(out, segmentArtifact{
			Stream:   stream,
			Kind:     kind,
			TempPath: segmentPath,
			FirstNS:  writeResult.FirstRecvNS,
			Packets:  writeResult.RTPPackets,
		})
	}

	for idx := range out {
		seg := &out[idx]
		logPath := filepath.Join(streamsDir, seg.Stream.StreamID+".rtplog")
		profile, err := analyzeTimeline(logPath, seg.Stream.PrimarySSRC, seg.Stream.ClockRate)
		if err != nil {
			seg.FirstTimelineNS = int64(seg.FirstNS)
			continue
		}
		seg.TimelineProfile = profile
		seg.TimelineAdjustNS = recommendedTimelineStartAdjustmentNS(profile)
		seg.FirstTimelineNS = int64(seg.FirstNS) + seg.TimelineAdjustNS
		if seg.FirstTimelineNS < 0 {
			seg.FirstTimelineNS = 0
		}
	}
	return out, nil
}

func composeSingleTrackMKV(inputPath, outputPath, kind string) error {
	args := []string{
		"-y",
		"-v", "error",
		"-copyts",
		"-i", inputPath,
	}
	switch kind {
	case "audio":
		args = append(args, "-map", "0:a:0")
	case "video":
		args = append(args, "-map", "0:v:0")
	default:
		args = append(args, "-map", "0")
	}
	args = append(args, "-c", "copy", outputPath)
	return runCommand("ffmpeg", args...)
}

func mergeSegments(
	outputPath,
	titleOverride string,
	sess session.Session,
	segments []segmentArtifact,
	streamPlans []StreamPlan,
	workDir string,
) error {
	pathByID := map[string]string{}
	for _, seg := range segments {
		pathByID[seg.Stream.StreamID] = seg.TempPath
	}

	args := []string{"-y", "-v", "error"}
	for _, plan := range streamPlans {
		if math.Abs(plan.OffsetSeconds) > 1e-6 {
			args = append(args, "-itsoffset", fmt.Sprintf("%.6f", plan.OffsetSeconds))
		}
		args = append(args, "-i", pathByID[plan.StreamID])
	}

	videoIndex := 0
	audioIndex := 0
	for idx, plan := range streamPlans {
		streamTitle := plan.StreamID
		if strings.TrimSpace(plan.ParticipantName) != "" {
			streamTitle = plan.ParticipantName
		}
		streamMetadata := streamMetadataEntries(plan)
		switch plan.Kind {
		case "video":
			args = append(args, "-map", fmt.Sprintf("%d:v:0", idx))
			args = append(args, fmt.Sprintf("-metadata:s:v:%d", videoIndex), "title="+streamTitle)
			for _, entry := range streamMetadata {
				args = append(args, fmt.Sprintf("-metadata:s:v:%d", videoIndex), entry)
			}
			videoIndex++
		case "audio":
			args = append(args, "-map", fmt.Sprintf("%d:a:0", idx))
			args = append(args, fmt.Sprintf("-metadata:s:a:%d", audioIndex), "title="+streamTitle)
			for _, entry := range streamMetadata {
				args = append(args, fmt.Sprintf("-metadata:s:a:%d", audioIndex), entry)
			}
			audioIndex++
		}
	}

	title := titleOverride
	if strings.TrimSpace(title) == "" {
		title = "Cassini Artifact Remux " + sess.SessionID
	}

	reportPath, err := writeEmbeddedReportFile(workDir, sess, streamPlans)
	if err != nil {
		return err
	}

	args = append(args, "-c", "copy")
	for _, entry := range containerMetadataEntries(title, sess, streamPlans) {
		args = append(args, "-metadata", entry)
	}
	args = append(args,
		"-attach", reportPath,
		"-metadata:s:t:0", "mimetype=application/json",
		"-metadata:s:t:0", "filename="+embeddedReportFile,
		outputPath,
	)
	return runCommand("ffmpeg", args...)
}

func planSegments(segments []segmentArtifact) []PlannedInput {
	inputs := make([]StreamInput, 0, len(segments))
	for idx := range segments {
		seg := &segments[idx]
		sourceStart, _ := probeMinStreamStartSeconds(seg.TempPath)
		seg.SourceStart = sourceStart
		inputs = append(inputs, StreamInput{
			StreamID:        seg.Stream.StreamID,
			LTID:            seg.Stream.LTID,
			Kind:            seg.Kind,
			Codec:           seg.Stream.Codec,
			FirstRecvNS:     seg.FirstNS,
			FirstTimelineNS: seg.FirstTimelineNS,
			SourceStart:     sourceStart,
		})
	}
	planned := PlanMerge(inputs)
	if len(planned) == 1 {
		planned[0].OffsetSeconds = 0
	}
	return planned
}

func buildStreamPlans(sess session.Session, segments []segmentArtifact, planned []PlannedInput) []StreamPlan {
	segmentsByID := map[string]segmentArtifact{}
	for _, seg := range segments {
		segmentsByID[seg.Stream.StreamID] = seg
	}
	logicalByLTID := map[string]session.LogicalTrack{}
	for _, logical := range sess.LogicalTracks {
		logicalByLTID[logical.LTID] = logical
	}
	participantDisplayByID := map[string]string{}
	for _, participant := range sess.Participants {
		participantDisplayByID[participant.PID] = participant.Display
	}

	out := make([]StreamPlan, 0, len(planned))
	for _, item := range planned {
		seg, ok := segmentsByID[item.StreamID]
		if !ok {
			continue
		}
		logical := logicalByLTID[item.LTID]
		participantID := logical.ParticipantID
		out = append(out, StreamPlan{
			StreamID:               item.StreamID,
			LTID:                   item.LTID,
			Kind:                   item.Kind,
			Codec:                  item.Codec,
			ParticipantID:          participantID,
			ParticipantName:        participantDisplayByID[participantID],
			MID:                    seg.Stream.MID,
			RID:                    seg.Stream.RID,
			ClockRate:              seg.Stream.ClockRate,
			PT:                     seg.Stream.PT,
			RTPPackets:             seg.Packets,
			FirstRecvNS:            seg.FirstNS,
			FirstTimelineNS:        seg.FirstTimelineNS,
			TimelineAdjustNS:       seg.TimelineAdjustNS,
			TimelineSamples:        seg.TimelineProfile.Samples,
			TimelineRawDurationNS:  seg.TimelineProfile.RawDurationNS,
			TimelineCorrDurationNS: seg.TimelineProfile.CorrectedDurationNS,
			SourceStartSeconds:     item.SourceStart,
			OffsetSeconds:          item.OffsetSeconds,
		})
	}
	return out
}

func runCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		errText := strings.TrimSpace(string(out))
		if errText == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, errText)
	}
	return nil
}

func probeMinStreamStartSeconds(path string) (float64, bool) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "stream=start_time",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}

	lines := strings.Split(string(out), "\n")
	min := math.MaxFloat64
	have := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "N/A") {
			continue
		}
		value, convErr := strconv.ParseFloat(line, 64)
		if convErr != nil {
			continue
		}
		if value < min {
			min = value
		}
		have = true
	}
	if !have {
		return 0, false
	}
	return min, true
}

func probeHasUsableStream(path string) bool {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "stream=codec_type",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(strings.ToLower(line)) {
		case "audio", "video":
			return true
		}
	}
	return false
}
