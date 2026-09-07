package remux

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gocassini/pkg/core/session"
)

const (
	MeetingFormatVersion = "cassini-meeting-mkv-v1"
	embeddedReportSchema = "cassini.embedded-report.v1"
	embeddedReportFile   = "cassini-report.v1.json"
)

type embeddedReport struct {
	Schema      string               `json:"schema"`
	GeneratedAt string               `json:"generated_at"`
	Session     embeddedSession      `json:"session"`
	Artifact    embeddedArtifactPlan `json:"artifact_remux"`
}

type embeddedSession struct {
	Version        int                    `json:"version"`
	SessionID      string                 `json:"session_id"`
	StartedWallUTC string                 `json:"started_wall_utc"`
	Platform       embeddedPlatform       `json:"platform"`
	Participants   []session.Participant  `json:"participants,omitempty"`
	LogicalTracks  []embeddedLogicalTrack `json:"logical_tracks,omitempty"`
	PacketStreams  []embeddedPacketStream `json:"packet_streams,omitempty"`
}

type embeddedPlatform struct {
	Name             string                   `json:"name"`
	Deployment       string                   `json:"deployment"`
	RecorderIdentity session.RecorderIdentity `json:"recorder_identity"`
}

type embeddedLogicalTrack struct {
	LTID          string `json:"ltid"`
	Kind          string `json:"kind"`
	Source        string `json:"source"`
	ParticipantID string `json:"participant_id"`
	MID           string `json:"mid"`
	RID           string `json:"rid"`
}

type embeddedPacketStream struct {
	StreamID     string            `json:"stream_id"`
	LTID         string            `json:"ltid"`
	MID          string            `json:"mid"`
	RID          string            `json:"rid"`
	PrimarySSRC  uint32            `json:"primary_ssrc"`
	CSRC         uint32            `json:"csrc"`
	Codec        string            `json:"codec"`
	ClockRate    uint32            `json:"clock_rate"`
	PT           uint8             `json:"pt"`
	FmtpSnapshot map[string]string `json:"fmtp_snapshot,omitempty"`
}

type embeddedArtifactPlan struct {
	Used                  bool            `json:"used"`
	Segments              int             `json:"segments"`
	AdjustedStreams       int             `json:"adjusted_streams"`
	TotalAdjustNS         int64           `json:"total_adjust_ns"`
	MaxAbsAdjustNS        int64           `json:"max_abs_adjust_ns"`
	MeanAdjustNSPerStream int64           `json:"mean_adjust_ns_per_stream"`
	EmbeddedReportFile    string          `json:"embedded_report_file"`
	PortableMeetingFormat string          `json:"portable_meeting_format"`
	StreamPlans           []StreamPlan    `json:"stream_plans"`
	SkippedStreams        []SkippedStream `json:"skipped_streams,omitempty"`
}

func buildEmbeddedReport(sess session.Session, plans []StreamPlan, skipped []SkippedStream) embeddedReport {
	totalAdjustNS, maxAbsAdjustNS, adjustedStreams := SummarizePlanAdjustments(plans)

	logicalTracks := make([]embeddedLogicalTrack, 0, len(sess.LogicalTracks))
	for _, logical := range sess.LogicalTracks {
		logicalTracks = append(logicalTracks, embeddedLogicalTrack{
			LTID:          logical.LTID,
			Kind:          logical.Kind,
			Source:        logical.Source,
			ParticipantID: logical.ParticipantID,
			MID:           logical.MID,
			RID:           logical.RID,
		})
	}

	packetStreams := make([]embeddedPacketStream, 0, len(sess.PacketStreams))
	for _, stream := range sess.PacketStreams {
		packetStreams = append(packetStreams, embeddedPacketStream{
			StreamID:     stream.StreamID,
			LTID:         stream.LTID,
			MID:          stream.MID,
			RID:          stream.RID,
			PrimarySSRC:  stream.PrimarySSRC,
			CSRC:         stream.CSRC,
			Codec:        stream.Codec,
			ClockRate:    stream.ClockRate,
			PT:           stream.PT,
			FmtpSnapshot: copyStringMap(stream.FmtpSnapshot),
		})
	}

	return embeddedReport{
		Schema:      embeddedReportSchema,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Session: embeddedSession{
			Version:        sess.Version,
			SessionID:      sess.SessionID,
			StartedWallUTC: sess.StartedWallUTC,
			Platform: embeddedPlatform{
				Name:             sess.Platform.Name,
				Deployment:       sess.Platform.Deployment,
				RecorderIdentity: sess.Platform.RecorderIdentity,
			},
			Participants:  append([]session.Participant(nil), sess.Participants...),
			LogicalTracks: logicalTracks,
			PacketStreams: packetStreams,
		},
		Artifact: embeddedArtifactPlan{
			Used:                  true,
			Segments:              len(plans),
			AdjustedStreams:       adjustedStreams,
			TotalAdjustNS:         totalAdjustNS,
			MaxAbsAdjustNS:        maxAbsAdjustNS,
			MeanAdjustNSPerStream: meanAdjustNS(totalAdjustNS, len(plans)),
			EmbeddedReportFile:    embeddedReportFile,
			PortableMeetingFormat: MeetingFormatVersion,
			StreamPlans:           append([]StreamPlan(nil), plans...),
			SkippedStreams:        append([]SkippedStream(nil), skipped...),
		},
	}
}

func writeEmbeddedReportFile(workDir string, sess session.Session, plans []StreamPlan, skipped []SkippedStream) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("embedded report requires a work dir")
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("create work dir for embedded report: %w", err)
	}

	reportPath := filepath.Join(workDir, embeddedReportFile)
	body, err := json.MarshalIndent(buildEmbeddedReport(sess, plans, skipped), "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal embedded report: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(reportPath, body, 0o644); err != nil {
		return "", fmt.Errorf("write embedded report: %w", err)
	}
	return reportPath, nil
}

func containerMetadataEntries(title string, sess session.Session, plans []StreamPlan) []string {
	totalAdjustNS, maxAbsAdjustNS, adjustedStreams := SummarizePlanAdjustments(plans)
	return []string{
		"title=" + title,
		"session_id=" + sess.SessionID,
		"cassini_format=" + MeetingFormatVersion,
		"cassini_embedded_report=" + embeddedReportFile,
		"session_started_at=" + sess.StartedWallUTC,
		"participant_count=" + strconv.Itoa(len(sess.Participants)),
		"logical_track_count=" + strconv.Itoa(len(sess.LogicalTracks)),
		"packet_stream_count=" + strconv.Itoa(len(sess.PacketStreams)),
		"artifact_remux_segments=" + strconv.Itoa(len(plans)),
		"artifact_remux_adjusted_streams=" + strconv.Itoa(adjustedStreams),
		"artifact_remux_total_adjust_ns=" + strconv.FormatInt(totalAdjustNS, 10),
		"artifact_remux_max_abs_adjust_ns=" + strconv.FormatInt(maxAbsAdjustNS, 10),
	}
}

func streamMetadataEntries(plan StreamPlan) []string {
	entries := []string{
		"ltid=" + plan.LTID,
		"stream_id=" + plan.StreamID,
		"kind=" + plan.Kind,
		"codec=" + plan.Codec,
		"timeline_adjust_ns=" + strconv.FormatInt(plan.TimelineAdjustNS, 10),
		"timeline_samples=" + strconv.Itoa(plan.TimelineSamples),
		"source_start_seconds=" + formatSeconds(plan.SourceStartSeconds),
		"offset_seconds=" + formatSeconds(plan.OffsetSeconds),
		"rtp_packets=" + strconv.Itoa(plan.RTPPackets),
		"pt=" + strconv.FormatUint(uint64(plan.PT), 10),
	}
	if plan.ParticipantID != "" {
		entries = append(entries, "participant_id="+plan.ParticipantID)
	}
	if plan.ParticipantName != "" {
		entries = append(entries, "participant_name="+plan.ParticipantName)
	}
	if plan.MID != "" {
		entries = append(entries, "mid="+plan.MID)
	}
	if plan.RID != "" {
		entries = append(entries, "rid="+plan.RID)
	}
	if plan.ClockRate != 0 {
		entries = append(entries, "clock_rate="+strconv.FormatUint(uint64(plan.ClockRate), 10))
	}
	// The RTP base travels in the MKV, not only in the embedded report, so
	// source-audio ingestion can read it with the same ffprobe call it already
	// makes for participant_id (internal/transcribe/audio.go) instead of
	// reopening the session artifact. Emitted only when a packet was actually
	// observed: 0 is a legal RTP timestamp, so a missing tag and a zero tag
	// must stay distinguishable.
	if plan.FirstRTPSet {
		// Diagnostics only: Janus rewrites subscriber timestamps, so this is
		// not the sender's clock. See StreamPlan.FirstRTPTimestamp.
		entries = append(entries, "first_rtp_timestamp="+strconv.FormatInt(plan.FirstRTPTimestamp, 10))
	}
	// The wall-clock anchor source-audio ingestion places against. Emitted into
	// the MKV so the build reads it with the ffprobe call it already makes,
	// rather than reopening the session artifact.
	if plan.FirstPacketWallMS > 0 {
		entries = append(entries, "first_packet_wall_ms="+strconv.FormatInt(plan.FirstPacketWallMS, 10))
		entries = append(entries, "first_timeline_ns="+strconv.FormatInt(plan.FirstTimelineNS, 10))
	}
	return entries
}

func SummarizePlanAdjustments(plans []StreamPlan) (int64, int64, int) {
	totalAdjustNS := int64(0)
	maxAbsAdjustNS := int64(0)
	adjustedStreams := 0
	for _, plan := range plans {
		totalAdjustNS += plan.TimelineAdjustNS
		absAdjust := plan.TimelineAdjustNS
		if absAdjust < 0 {
			absAdjust = -absAdjust
		}
		if absAdjust > maxAbsAdjustNS {
			maxAbsAdjustNS = absAdjust
		}
		if plan.TimelineAdjustNS != 0 {
			adjustedStreams++
		}
	}
	return totalAdjustNS, maxAbsAdjustNS, adjustedStreams
}

func meanAdjustNS(totalAdjustNS int64, streams int) int64 {
	if streams == 0 {
		return 0
	}
	return totalAdjustNS / int64(streams)
}

func formatSeconds(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}

func copyStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
