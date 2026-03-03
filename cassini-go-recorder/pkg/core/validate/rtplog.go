package validate

import (
	"errors"
	"fmt"
	"io"

	"gocassini/pkg/core/store"

	"github.com/pion/rtp"
)

type IssueCode string

const (
	IssueRecvNonMonotonic  IssueCode = "recv_non_monotonic"
	IssueRTPUnmarshal      IssueCode = "rtp_unmarshal_failed"
	IssuePayloadTypeChange IssueCode = "payload_type_mismatch"
)

type Issue struct {
	Code        IssueCode `json:"code"`
	Message     string    `json:"message"`
	RecordIndex int       `json:"record_index"`
	RecvMonoNS  uint64    `json:"recv_mono_ns"`
}

type LogReport struct {
	Path       string  `json:"path"`
	Records    int     `json:"records"`
	RTP        int     `json:"rtp"`
	RTCP       int     `json:"rtcp"`
	FirstRecv  uint64  `json:"first_recv_mono_ns"`
	LastRecv   uint64  `json:"last_recv_mono_ns"`
	HeaderPT   uint8   `json:"header_pt"`
	IssueCount int     `json:"issue_count"`
	Issues     []Issue `json:"issues,omitempty"`
}

func CheckLog(path string) (LogReport, error) {
	reader, err := store.OpenReader(path)
	if err != nil {
		return LogReport{}, fmt.Errorf("open rtplog: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()

	header := reader.Header()
	report := LogReport{
		Path:     path,
		HeaderPT: header.Stream.PT,
	}
	var prevRecv uint64
	hasPrev := false
	index := 0

	for {
		record, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return LogReport{}, fmt.Errorf("read record: %w", err)
		}
		report.Records++
		if !hasPrev || record.RecvMonoNS < report.FirstRecv {
			report.FirstRecv = record.RecvMonoNS
		}
		if record.RecvMonoNS > report.LastRecv {
			report.LastRecv = record.RecvMonoNS
		}
		if hasPrev && record.RecvMonoNS < prevRecv {
			report.addIssue(Issue{
				Code:        IssueRecvNonMonotonic,
				Message:     fmt.Sprintf("recv timestamp moved backwards (%d -> %d)", prevRecv, record.RecvMonoNS),
				RecordIndex: index,
				RecvMonoNS:  record.RecvMonoNS,
			})
		}
		prevRecv = record.RecvMonoNS
		hasPrev = true

		switch record.Kind {
		case store.KindRTP:
			report.RTP++
			var pkt rtp.Packet
			if err := pkt.Unmarshal(record.WireBytes); err != nil {
				report.addIssue(Issue{
					Code:        IssueRTPUnmarshal,
					Message:     err.Error(),
					RecordIndex: index,
					RecvMonoNS:  record.RecvMonoNS,
				})
				index++
				continue
			}
			if report.HeaderPT != 0 && pkt.PayloadType != report.HeaderPT {
				report.addIssue(Issue{
					Code:        IssuePayloadTypeChange,
					Message:     fmt.Sprintf("packet payload type %d != header payload type %d", pkt.PayloadType, report.HeaderPT),
					RecordIndex: index,
					RecvMonoNS:  record.RecvMonoNS,
				})
			}
		case store.KindRTCP:
			report.RTCP++
		}
		index++
	}

	report.IssueCount = len(report.Issues)
	return report, nil
}

func (r *LogReport) addIssue(issue Issue) {
	const maxIssues = 64
	if len(r.Issues) >= maxIssues {
		return
	}
	r.Issues = append(r.Issues, issue)
}
