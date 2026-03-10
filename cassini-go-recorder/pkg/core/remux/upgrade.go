package remux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gocassini/pkg/core/session"
)

type UpgradeOptions struct {
	WorkDir  string
	KeepWork bool
	Title    string
}

type legacyRecorderReport struct {
	StartedAt       string `json:"started_at"`
	GuestName       string `json:"guest_name"`
	RoomToken       string `json:"room_token"`
	SessionArtifact struct {
		SessionID string `json:"session_id"`
	} `json:"session_artifact"`
	ArtifactRemux struct {
		Used                  bool         `json:"used"`
		Segments              int          `json:"segments"`
		AdjustedStreams       int          `json:"adjusted_streams"`
		TotalAdjustNS         int64        `json:"total_adjust_ns"`
		MaxAbsAdjustNS        int64        `json:"max_abs_adjust_ns"`
		MeanAdjustNSPerStream int64        `json:"mean_adjust_ns_per_stream"`
		StreamPlans           []StreamPlan `json:"stream_plans"`
	} `json:"artifact_remux"`
}

type probedMeetingMKV struct {
	Streams []probedMeetingStream `json:"streams"`
	Format  struct {
		Tags map[string]string `json:"tags"`
	} `json:"format"`
}

type probedMeetingStream struct {
	Index     int               `json:"index"`
	CodecType string            `json:"codec_type"`
	Tags      map[string]string `json:"tags"`
}

type legacyUpgradeStream struct {
	sourceIndex int
	kind        string
	typeIndex   int
	streamTitle string
	streamPlan  StreamPlan
}

func UpgradeLegacyMeetingMKV(inputPath, reportPath, outputPath string, opts UpgradeOptions) error {
	if strings.TrimSpace(inputPath) == "" {
		return fmt.Errorf("missing input mkv path")
	}
	if strings.TrimSpace(reportPath) == "" {
		return fmt.Errorf("missing legacy report path")
	}
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("missing output mkv path")
	}

	report, err := loadLegacyRecorderReport(reportPath)
	if err != nil {
		return err
	}
	if !report.ArtifactRemux.Used || len(report.ArtifactRemux.StreamPlans) == 0 {
		return fmt.Errorf("legacy report does not contain artifact remux stream plans: %s", reportPath)
	}

	meta, err := probeMeetingMKVMetadata(inputPath)
	if err != nil {
		return err
	}

	sessionID := firstNonEmpty(
		metadataValue(meta.Format.Tags, "SESSION_ID", "session_id"),
		report.SessionArtifact.SessionID,
	)
	if sessionID == "" {
		return fmt.Errorf("could not determine session id from %s or %s", inputPath, reportPath)
	}

	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = firstNonEmpty(
			metadataValue(meta.Format.Tags, "TITLE", "title"),
			"Cassini Go Recording "+sessionID,
		)
	}

	workDir, cleanup, err := prepareWorkDir(opts.WorkDir, opts.KeepWork)
	if err != nil {
		return err
	}
	defer cleanup()

	streamPlanByID := make(map[string]StreamPlan, len(report.ArtifactRemux.StreamPlans))
	for _, plan := range report.ArtifactRemux.StreamPlans {
		streamPlanByID[plan.StreamID] = plan
	}

	streams, sess, err := buildLegacyUpgradeSession(meta.Streams, streamPlanByID, sessionID, report.StartedAt, report.GuestName)
	if err != nil {
		return err
	}
	reportFile, err := writeEmbeddedReportFile(workDir, sess, report.ArtifactRemux.StreamPlans)
	if err != nil {
		return err
	}

	if dir := filepath.Dir(outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create output dir: %w", err)
		}
	}

	args := []string{"-y", "-v", "error", "-i", inputPath}
	for _, stream := range streams {
		args = append(args, "-map", fmt.Sprintf("0:%d", stream.streamPlanIndex()))
	}
	args = append(args, "-c", "copy")
	for _, entry := range containerMetadataEntries(title, sess, report.ArtifactRemux.StreamPlans) {
		args = append(args, "-metadata", entry)
	}
	for _, stream := range streams {
		specifier := fmt.Sprintf("-metadata:s:%s:%d", stream.kindSpecifier(), stream.typeIndex)
		args = append(args, specifier, "title="+stream.streamTitle)
		for _, entry := range streamMetadataEntries(stream.streamPlan) {
			args = append(args, specifier, entry)
		}
	}
	args = append(args,
		"-attach", reportFile,
		"-metadata:s:t:0", "mimetype=application/json",
		"-metadata:s:t:0", "filename="+embeddedReportFile,
		outputPath,
	)
	return runCommand("ffmpeg", args...)
}

func loadLegacyRecorderReport(path string) (legacyRecorderReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return legacyRecorderReport{}, fmt.Errorf("read legacy report: %w", err)
	}
	var report legacyRecorderReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return legacyRecorderReport{}, fmt.Errorf("parse legacy report json: %w", err)
	}
	return report, nil
}

func probeMeetingMKVMetadata(path string) (probedMeetingMKV, error) {
	cmd := []string{
		"-v", "error",
		"-show_entries", "format_tags:stream=index,codec_type:stream_tags",
		"-of", "json",
		path,
	}
	raw, err := execCommandOutput("ffprobe", cmd...)
	if err != nil {
		return probedMeetingMKV{}, fmt.Errorf("ffprobe legacy mkv: %w", err)
	}
	var meta probedMeetingMKV
	if err := json.Unmarshal(raw, &meta); err != nil {
		return probedMeetingMKV{}, fmt.Errorf("parse ffprobe mkv json: %w", err)
	}
	return meta, nil
}

func buildLegacyUpgradeSession(
	streams []probedMeetingStream,
	streamPlanByID map[string]StreamPlan,
	sessionID string,
	startedAt string,
	guestName string,
) ([]legacyUpgradeStream, session.Session, error) {
	out := make([]legacyUpgradeStream, 0, len(streams))
	participantsByID := map[string]session.Participant{}
	logicalByLTID := map[string]session.LogicalTrack{}
	packetStreams := make([]session.PacketStream, 0, len(streamPlanByID))
	remaining := make(map[string]struct{}, len(streamPlanByID))
	for streamID := range streamPlanByID {
		remaining[streamID] = struct{}{}
	}

	videoIndex := 0
	audioIndex := 0
	startedMonoNS := uint64(0)
	for _, stream := range streams {
		kind := strings.ToLower(strings.TrimSpace(stream.CodecType))
		if kind != "audio" && kind != "video" {
			continue
		}
		streamID := metadataValue(stream.Tags, "STREAM_ID", "stream_id")
		if streamID == "" {
			return nil, session.Session{}, fmt.Errorf("stream %d is missing STREAM_ID tag", stream.Index)
		}
		plan, ok := streamPlanByID[streamID]
		if !ok {
			return nil, session.Session{}, fmt.Errorf("legacy report is missing stream plan for %s", streamID)
		}
		delete(remaining, streamID)

		typeIndex := audioIndex
		if kind == "video" {
			typeIndex = videoIndex
			videoIndex++
		} else {
			audioIndex++
		}

		participantID := firstNonEmpty(plan.ParticipantID, metadataValue(stream.Tags, "PARTICIPANT_ID", "participant_id"))
		participantName := firstNonEmpty(plan.ParticipantName, metadataValue(stream.Tags, "PARTICIPANT_NAME", "participant_name"))
		if participantID != "" {
			participantsByID[participantID] = session.Participant{
				PID:     participantID,
				Display: participantName,
			}
		}
		if plan.LTID != "" {
			logicalByLTID[plan.LTID] = session.LogicalTrack{
				LTID:          plan.LTID,
				Kind:          plan.Kind,
				Source:        "janus",
				ParticipantID: participantID,
				MID:           plan.MID,
				RID:           plan.RID,
				CreatedMonoNS: plan.FirstRecvNS,
			}
		}

		packetStreams = append(packetStreams, session.PacketStream{
			StreamID:    plan.StreamID,
			LTID:        plan.LTID,
			MID:         plan.MID,
			RID:         plan.RID,
			PrimarySSRC: 0,
			CSRC:        0,
			Codec:       plan.Codec,
			ClockRate:   plan.ClockRate,
			PT:          plan.PT,
			StartMonoNS: plan.FirstRecvNS,
		})
		if startedMonoNS == 0 || (plan.FirstRecvNS != 0 && plan.FirstRecvNS < startedMonoNS) {
			startedMonoNS = plan.FirstRecvNS
		}

		out = append(out, legacyUpgradeStream{
			sourceIndex: stream.Index,
			kind:        kind,
			typeIndex:   typeIndex,
			streamTitle: firstNonEmpty(metadataValue(stream.Tags, "TITLE", "title"), participantName, plan.StreamID),
			streamPlan:  plan,
		})
	}
	if len(remaining) != 0 {
		missing := make([]string, 0, len(remaining))
		for streamID := range remaining {
			missing = append(missing, streamID)
		}
		sort.Strings(missing)
		return nil, session.Session{}, fmt.Errorf("legacy mkv is missing %d planned streams: %s", len(missing), strings.Join(missing, ", "))
	}

	participants := make([]session.Participant, 0, len(participantsByID))
	for _, participant := range participantsByID {
		participants = append(participants, participant)
	}
	sort.Slice(participants, func(i, j int) bool {
		if participants[i].PID == participants[j].PID {
			return participants[i].Display < participants[j].Display
		}
		return participants[i].PID < participants[j].PID
	})

	logicalTracks := make([]session.LogicalTrack, 0, len(logicalByLTID))
	for _, logical := range logicalByLTID {
		logicalTracks = append(logicalTracks, logical)
	}
	sort.Slice(logicalTracks, func(i, j int) bool {
		return logicalTracks[i].LTID < logicalTracks[j].LTID
	})

	sort.Slice(packetStreams, func(i, j int) bool {
		return packetStreams[i].StreamID < packetStreams[j].StreamID
	})

	return out, session.Session{
		Version:        session.SchemaVersion,
		SessionID:      sessionID,
		StartedWallUTC: startedAt,
		StartedMonoNS:  startedMonoNS,
		Platform: session.Platform{
			Name: "nextcloud-talk",
			Room: "",
			RecorderIdentity: session.RecorderIdentity{
				Display: guestName,
				Silent:  false,
			},
		},
		LogicalTracks: logicalTracks,
		PacketStreams: packetStreams,
		Participants:  participants,
	}, nil
}

func (s legacyUpgradeStream) kindSpecifier() string {
	if s.kind == "video" {
		return "v"
	}
	return "a"
}

func (s legacyUpgradeStream) streamPlanIndex() int {
	return s.sourceIndex
}

func metadataValue(tags map[string]string, keys ...string) string {
	for _, key := range keys {
		if tags == nil {
			continue
		}
		if value, ok := tags[key]; ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func execCommandOutput(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		errText := strings.TrimSpace(string(out))
		if errText == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", err, errText)
	}
	return out, nil
}
