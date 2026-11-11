package talk

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// seenParticipant is one local account the recorder saw in the call. It is
// written beside the run bundle so the operator can merge it into the saved
// audience before publication (#479). The recorder has left the call by the
// time the stop capture runs, so Talk no longer has the late joiners to ask;
// the recorder's monotonic inCallEver identities are the only source that does.
type seenParticipant struct {
	ActorType   string `json:"actorType"`
	ActorID     string `json:"actorId"`
	DisplayName string `json:"displayName,omitempty"`
}

const seenParticipantsFilename = "seen-participants.json"

// writeSeenParticipants writes the monotonic account list into the run bundle.
// Best effort: a failure here loses only the late-joiner enrichment, never the
// recording; the operator's start capture remains the fallback.
func (r *Recorder) writeSeenParticipants() {
	if r == nil || strings.TrimSpace(r.cfg.OutputPath) == "" {
		return
	}
	r.sessionMu.Lock()
	accounts := make([]seenParticipant, 0, len(r.seenParticipants))
	for _, identity := range r.seenParticipants {
		if strings.TrimSpace(identity.ParticipantID) == "" || identity.ActorType != "users" {
			continue
		}
		accounts = append(accounts, seenParticipant{
			ActorType:   identity.ActorType,
			ActorID:     strings.TrimSpace(identity.ParticipantID),
			DisplayName: strings.TrimSpace(identity.DisplayName),
		})
	}
	r.sessionMu.Unlock()
	if len(accounts) == 0 {
		return
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ActorID < accounts[j].ActorID })
	raw, err := json.Marshal(accounts)
	if err != nil {
		log.Printf("seen participants marshal failed: %v", err)
		return
	}
	path := filepath.Join(filepath.Dir(r.cfg.OutputPath), seenParticipantsFilename)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		log.Printf("seen participants write failed path=%s: %v", path, err)
	}
}
