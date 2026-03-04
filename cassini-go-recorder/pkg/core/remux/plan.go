package remux

import (
	"path/filepath"
	"sort"
	"strings"
)

type StreamInput struct {
	StreamID    string
	LTID        string
	Kind        string
	Codec       string
	FirstRecvNS uint64
	SourceStart float64
}

type PlannedInput struct {
	StreamInput
	OffsetSeconds float64
}

func PlanMerge(inputs []StreamInput) []PlannedInput {
	if len(inputs) == 0 {
		return nil
	}

	ordered := make([]StreamInput, 0, len(inputs))
	for _, in := range inputs {
		ordered = append(ordered, in)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].FirstRecvNS == ordered[j].FirstRecvNS {
			return ordered[i].StreamID < ordered[j].StreamID
		}
		return ordered[i].FirstRecvNS < ordered[j].FirstRecvNS
	})

	base := ordered[0].FirstRecvNS
	out := make([]PlannedInput, 0, len(ordered))
	for _, in := range ordered {
		desired := float64(in.FirstRecvNS-base) / 1e9
		out = append(out, PlannedInput{
			StreamInput:   in,
			OffsetSeconds: desired - in.SourceStart,
		})
	}
	return out
}

func KindFromCodec(codec string) string {
	lc := strings.ToLower(strings.TrimSpace(codec))
	switch {
	case strings.HasPrefix(lc, "audio/"), lc == "opus":
		return "audio"
	case strings.HasPrefix(lc, "video/"), lc == "vp8", lc == "vp9", lc == "h264", lc == "av1":
		return "video"
	default:
		return "unknown"
	}
}

func ElementaryExtension(codec string) string {
	lc := strings.ToLower(strings.TrimSpace(codec))
	switch {
	case strings.Contains(lc, "opus"):
		return ".ogg"
	case strings.Contains(lc, "vp8"), strings.Contains(lc, "vp9"), strings.Contains(lc, "av1"):
		return ".ivf"
	case strings.Contains(lc, "h264"):
		return ".h264"
	default:
		return ""
	}
}

func StreamTempBase(dir, streamID, codec string) string {
	ext := ElementaryExtension(codec)
	if ext == "" {
		return filepath.Join(dir, streamID)
	}
	return filepath.Join(dir, streamID+ext)
}
