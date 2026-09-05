package operator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cassini-operator/internal/operator/appapi"
	"github.com/oklog/ulid/v2"
)

const capturePieceBytes = 4 << 20
const capturePieceLimit = 1024

type captureTransferManifest struct {
	Sidecar captureSidecar      `json:"sidecar"`
	Pieces  map[string][]string `json:"pieces"`
}

func syncCaptureDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func syncCaptureParents(dir string) error {
	// session, owner, room, root, and root's parent: newly created directory
	// entries must be durable too, before the browser discards its source.
	for level := 0; level < 5; level++ {
		if err := syncCaptureDir(dir); err != nil {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

func (s *Store) captureRecordingRoom(ctx context.Context, id string) (string, error) {
	var room string
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(talk_binding, '$.room_token') FROM jobs WHERE id = ? AND record_started_at IS NOT NULL`, id).Scan(&room)
	return room, err
}

func (rt *Runtime) captureRecordingHandler(isMember roomMembershipChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		owner, room := appapi.UserID(r.Context()), r.URL.Query().Get("room")
		if !sourceCaptureEnabled() || owner == "" {
			http.Error(w, "capture unavailable", 403)
			return
		}
		if !captureSafeName.MatchString(room) || room == "." || room == ".." {
			http.Error(w, "invalid room", 400)
			return
		}
		if rt.store == nil {
			http.Error(w, "recording unavailable", 503)
			return
		}
		if isMember != nil {
			member, err := isMember(r.Context(), owner, room)
			if err != nil {
				http.Error(w, "membership unavailable", 503)
				return
			}
			if !member {
				http.Error(w, "not a room participant", 403)
				return
			}
		}
		rows, err := rt.store.db.QueryContext(r.Context(), `SELECT id,stage,state FROM jobs WHERE json_extract(talk_binding,'$.room_token') = ? AND record_started_at IS NOT NULL AND record_finished_at IS NULL`, room)
		if err != nil {
			http.Error(w, "recording unavailable", 503)
			return
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			var id, stage, state string
			if err := rows.Scan(&id, &stage, &state); err != nil {
				http.Error(w, "recording unavailable", 503)
				return
			}
			if recordingIsLive(stage, state) {
				ids = append(ids, id)
			}
		}
		if rows.Err() != nil {
			http.Error(w, "recording unavailable", 503)
			return
		}
		if len(ids) != 1 {
			http.Error(w, "no unique active recording", 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"recordingId": ids[0]})
	}
}

// A session is private to the authenticated owner and one server recording.
// The on-disk layout stays room/owner/session so existing quotas and retention
// cover incomplete transfers as well as committed captures.
func transferDir(root, room, owner, recording, session string) string {
	sum := sha256.Sum256([]byte(recording + "\x00" + session))
	return filepath.Join(filepath.Dir(captureUploadDir(root, room, owner, 1)), "session-"+hex.EncodeToString(sum[:]))
}

func validCaptureHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func (rt *Runtime) captureTransferHandler(isMember roomMembershipChecker, logger *log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" && r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		owner := strings.TrimSpace(appapi.UserID(r.Context()))
		if !sourceCaptureEnabled() || owner == "" {
			http.Error(w, "capture unavailable", 403)
			return
		}
		query := r.URL.Query()
		room, recording, session := query.Get("room"), query.Get("recording"), query.Get("session")
		for _, value := range []string{room, recording, session} {
			if !captureSafeName.MatchString(value) || value == "." || value == ".." {
				http.Error(w, "invalid identity", 400)
				return
			}
		}
		if rt.store == nil {
			http.Error(w, "recording unavailable", 503)
			return
		}
		actualRoom, err := rt.store.captureRecordingRoom(r.Context(), recording)
		if err != nil {
			status := 503
			if errors.Is(err, sql.ErrNoRows) {
				status = 409
			}
			http.Error(w, "recording unavailable", status)
			return
		}
		if room != actualRoom {
			http.Error(w, "recording belongs to another room", 403)
			return
		}
		if isMember != nil {
			member, err := isMember(r.Context(), owner, room)
			if err != nil {
				http.Error(w, "membership unavailable", 503)
				return
			}
			if !member {
				http.Error(w, "not a room participant", 403)
				return
			}
		}
		dir := transferDir(rt.cfg.CaptureRoot, room, owner, recording, session)
		if r.Method == "GET" {
			capturePromotionMu.Lock()
			defer capturePromotionMu.Unlock()
			entries, err := os.ReadDir(dir)
			if err != nil && !os.IsNotExist(err) {
				http.Error(w, "storage unavailable", 503)
				return
			}
			pieces := []string{}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".part") {
					pieces = append(pieces, strings.TrimSuffix(entry.Name(), ".part"))
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, committedErr := os.Stat(filepath.Join(dir, captureSidecarName))
			_ = json.NewEncoder(w).Encode(map[string]any{"pieces": pieces, "committed": committedErr == nil})
			return
		}
		if query.Get("op") == "commit" {
			r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
			var manifest captureTransferManifest
			if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
				http.Error(w, "invalid manifest", 400)
				return
			}
			capturePromotionMu.Lock()
			defer capturePromotionMu.Unlock()
			rt.commitCaptureTransfer(w, r, manifest, dir, room, owner, recording, session, logger)
			return
		}
		hash := query.Get("piece")
		if !validCaptureHash(hash) {
			http.Error(w, "invalid piece hash", 400)
			return
		}
		// The proxy receives one bounded multipart file, preserving its ordinary
		// session authentication without relying on PHP streaming a large body.
		r.Body = http.MaxBytesReader(w, r.Body, capturePieceBytes+(64<<10))
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, "expected multipart", 400)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			http.Error(w, "missing piece", 503)
			return
		}
		data, err := io.ReadAll(io.LimitReader(part, capturePieceBytes+1))
		if err != nil {
			http.Error(w, "truncated piece", 503)
			return
		}
		if len(data) == 0 || len(data) > capturePieceBytes {
			http.Error(w, "piece size", 413)
			return
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != hash {
			http.Error(w, "piece hash mismatch", 422)
			return
		}
		// Network reads finish before taking the storage/retention lock.
		capturePromotionMu.Lock()
		defer capturePromotionMu.Unlock()
		path := filepath.Join(dir, hash+".part")
		if stored, err := os.ReadFile(path); err == nil {
			if !bytes.Equal(stored, data) {
				http.Error(w, "stored piece conflicts", 409)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		} else if !os.IsNotExist(err) {
			http.Error(w, "storage unavailable", 503)
			return
		}
		if _, err := os.Stat(filepath.Join(dir, captureSidecarName)); err == nil {
			http.Error(w, "session already committed", 409)
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, "storage unavailable", 503)
			return
		}
		var total int64
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				http.Error(w, "storage unavailable", 503)
				return
			}
			total += info.Size()
		}
		if len(entries) >= capturePieceLimit || total+int64(len(data)) > captureMaxUploadBytes {
			http.Error(w, "session too large", 413)
			return
		}
		admission, refusal := admitCaptureUpload(rt.cfg.CaptureRoot, owner, int64(len(data)), captureLimitsFromEnv())
		if refusal != nil {
			refuseCaptureUpload(w, logger, owner, refusal.status, refusal.reason, refusal.message)
			return
		}
		defer releaseCaptureAdmission(admission.owner, admission.reserved)
		if err := os.MkdirAll(dir, 0750); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		// Write to a temporary name first: an interrupted write never advertises a
		// reusable piece in the inventory.
		temp := filepath.Join(dir, "piece.tmp")
		if err := writeFileSynced(temp, data); err != nil {
			_ = os.Remove(temp)
			http.Error(w, "storage unavailable", 503)
			return
		}
		if err := os.Rename(temp, path); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		if err := syncCaptureParents(dir); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (rt *Runtime) commitCaptureTransfer(w http.ResponseWriter, r *http.Request, manifest captureTransferManifest, dir, room, owner, recording, session string, logger *log.Logger) {
	sidecar := &manifest.Sidecar
	if err := validateSidecar(sidecar); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if sidecar.RoomToken != room || sidecar.RecordingID != recording || sidecar.SessionID != session {
		http.Error(w, "manifest identity mismatch", 409)
		return
	}
	// Ignore all server-owned fields when identifying a retry.
	sidecar.OwnerUserID, sidecar.ReceivedAt, sidecar.ReceiptID, sidecar.InputDigest = owner, "", "", ""
	canonical, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, "invalid manifest", 400)
		return
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	if raw, err := os.ReadFile(filepath.Join(dir, captureSidecarName)); err == nil {
		var stored captureSidecar
		if json.Unmarshal(raw, &stored) != nil || stored.InputDigest != digest {
			http.Error(w, "session manifest conflicts", 409)
			return
		}
		// Recovery also runs periodically if this request never reaches here.
		rt.noteCaptureArrival(&stored, owner, logger)
		writeCaptureCommitResponse(w, dir, &stored)
		return
	} else if !os.IsNotExist(err) {
		http.Error(w, "storage unavailable", 503)
		return
	}
	var size int64
	count := 0
	if len(manifest.Pieces) != len(sidecar.Segments) {
		http.Error(w, "segment set mismatch", 400)
		return
	}
	for _, segment := range sidecar.Segments {
		if segment.AudioName == captureSidecarName || segment.AudioName == "manifest.tmp" || segment.AudioName == "piece.tmp" || strings.HasSuffix(segment.AudioName, ".part") {
			http.Error(w, "reserved segment name", 400)
			return
		}
		pieces := manifest.Pieces[segment.AudioName]
		if len(pieces) == 0 {
			http.Error(w, "missing segment pieces", 409)
			return
		}
		for _, hash := range pieces {
			count++
			if count > capturePieceLimit || !validCaptureHash(hash) {
				http.Error(w, "invalid pieces", 400)
				return
			}
			info, err := os.Stat(filepath.Join(dir, hash+".part"))
			if err != nil {
				http.Error(w, "piece missing; refresh inventory", 409)
				return
			}
			size += info.Size()
			if size > captureMaxUploadBytes {
				http.Error(w, "session too large", 413)
				return
			}
		}
	}
	admission, refusal := admitCaptureUpload(rt.cfg.CaptureRoot, owner, size+int64(len(canonical))+4096, captureLimitsFromEnv())
	if refusal != nil {
		refuseCaptureUpload(w, logger, owner, refusal.status, refusal.reason, refusal.message)
		return
	}
	defer releaseCaptureAdmission(admission.owner, admission.reserved)
	// Reserve the assembled copy as well as the pieces already on disk. A
	// failed assembly is removed; the acknowledged pieces remain retryable.
	completed := false
	defer func() {
		if !completed {
			for _, segment := range sidecar.Segments {
				_ = os.Remove(filepath.Join(dir, segment.AudioName))
			}
		}
	}()
	for _, segment := range sidecar.Segments {
		path := filepath.Join(dir, segment.AudioName)
		out, err := os.Create(path)
		if err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		for _, hash := range manifest.Pieces[segment.AudioName] {
			data, readErr := os.ReadFile(filepath.Join(dir, hash+".part"))
			actual := sha256.Sum256(data)
			if readErr != nil || hex.EncodeToString(actual[:]) != hash {
				out.Close()
				http.Error(w, "piece integrity failure", 409)
				return
			}
			if _, err = out.Write(data); err != nil {
				out.Close()
				http.Error(w, "storage unavailable", 503)
				return
			}
		}
		err = out.Sync()
		closeErr := out.Close()
		if err != nil || closeErr != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
	}
	sidecar.ReceiptID, sidecar.ReceivedAt, sidecar.InputDigest = ulid.Make().String(), time.Now().UTC().Format(time.RFC3339), digest
	raw, err := json.Marshal(sidecar)
	if err != nil {
		http.Error(w, "invalid manifest", 400)
		return
	}
	temp := filepath.Join(dir, "manifest.tmp")
	if err := writeFileSynced(temp, raw); err != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	if err := os.Rename(temp, filepath.Join(dir, captureSidecarName)); err != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	// Once the manifest is visible, never delete its audio, even if fsync fails.
	completed = true
	if err := syncCaptureParents(dir); err != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	rt.noteCaptureArrival(sidecar, owner, logger)
	// A committed inventory directs retries straight to manifest verification.
	// The assembled audio is now the only retained copy.
	for _, pieces := range manifest.Pieces {
		for _, hash := range pieces {
			_ = os.Remove(filepath.Join(dir, hash+".part"))
		}
	}
	writeCaptureCommitResponse(w, dir, sidecar)
}

func writeCaptureCommitResponse(w http.ResponseWriter, dir string, sidecar *captureSidecar) {
	var total int64
	for _, segment := range sidecar.Segments {
		info, err := os.Stat(filepath.Join(dir, segment.AudioName))
		if err != nil {
			http.Error(w, "committed audio unavailable", 503)
			return
		}
		total += info.Size()
	}
	if err := syncCaptureParents(dir); err != nil {
		http.Error(w, "storage unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "accepted", "room": sidecar.RoomToken, "recordingId": sidecar.RecordingID,
		"sessionId": sidecar.SessionID, "captureDir": filepath.Base(dir),
		"segments": len(sidecar.Segments), "bytes": total,
	})
}
