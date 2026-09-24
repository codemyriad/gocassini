package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type captureStream struct {
	Index      int               `json:"index"`
	Kind       string            `json:"codec_type"`
	Codec      string            `json:"codec_name"`
	Channels   int               `json:"channels"`
	SampleRate string            `json:"sample_rate"`
	Tags       map[string]string `json:"tags"`
}

func probeCapture(ctx context.Context, path string) ([]captureStream, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-of", "json", path)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("probe capture: %w", err)
	}
	var p struct {
		Streams []captureStream `json:"streams"`
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if len(p.Streams) == 0 {
		return nil, errors.New("media has no streams")
	}
	return p.Streams, nil
}

type captureAudioFingerprint struct {
	Codec                               string
	Channels                            int
	SampleRate, Title, Language, Digest string
	Packets                             int
}

// Stream the packet inventory: long recordings must not load every packet's
// JSON into memory. Hash compressed bytes AND presentation/decode timestamps.
func fingerprintCaptureAudio(ctx context.Context, path string, streams []captureStream) ([]captureAudioFingerprint, error) {
	indexes := map[int]int{}
	var out []captureAudioFingerprint
	var hashes []hash.Hash
	for _, s := range streams {
		if s.Kind == "audio" {
			indexes[s.Index] = len(out)
			out = append(out, captureAudioFingerprint{Codec: s.Codec, Channels: s.Channels, SampleRate: s.SampleRate, Title: s.Tags["title"], Language: s.Tags["language"]})
			hashes = append(hashes, sha256.New())
		}
	}
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=stream_index,pts_time,dts_time,duration_time,data_hash", "-of", "json", path)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = pipe.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	dec := json.NewDecoder(pipe)
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	for dec.More() {
		key, e := dec.Token()
		if e != nil {
			return nil, e
		}
		if key != "packets" {
			var skip json.RawMessage
			if e = dec.Decode(&skip); e != nil {
				return nil, e
			}
			continue
		}
		if _, e = dec.Token(); e != nil {
			return nil, e
		}
		for dec.More() {
			var p struct {
				Index    int    `json:"stream_index"`
				PTS      string `json:"pts_time"`
				DTS      string `json:"dts_time"`
				Duration string `json:"duration_time"`
				Hash     string `json:"data_hash"`
			}
			if e = dec.Decode(&p); e != nil {
				return nil, e
			}
			i, ok := indexes[p.Index]
			if !ok || p.Hash == "" {
				return nil, errors.New("unidentified audio packet")
			}
			fmt.Fprintf(hashes[i], "%s|%s|%s|%s\n", p.PTS, p.DTS, p.Duration, p.Hash)
			out[i].Packets++
		}
		if _, e = dec.Token(); e != nil {
			return nil, e
		}
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, pipe)
	if err = cmd.Wait(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Digest = hex.EncodeToString(hashes[i].Sum(nil))
		if out[i].Packets == 0 {
			return nil, errors.New("audio stream contains no packets")
		}
	}
	return out, nil
}

// Rewrite metadata generically so unrecognised non-media fields survive.
func stripSessionVideo(path string, known map[string]bool) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s map[string]json.RawMessage
	if err = json.Unmarshal(b, &s); err != nil {
		return err
	}
	var version int
	if json.Unmarshal(s["version"], &version) != nil || version != 1 {
		return errors.New("unknown session schema")
	}
	var tracks, packets, transceivers []map[string]json.RawMessage
	if err = json.Unmarshal(s["logical_tracks"], &tracks); err != nil {
		return err
	}
	if err = json.Unmarshal(s["packet_streams"], &packets); err != nil {
		return err
	}
	str := func(m map[string]json.RawMessage, k string) string {
		var v string
		_ = json.Unmarshal(m[k], &v)
		return v
	}
	kinds := map[string]string{}
	keptTracks := []map[string]json.RawMessage{}
	for _, tr := range tracks {
		id, kind := str(tr, "ltid"), str(tr, "kind")
		if id == "" || (kind != "audio" && kind != "video") {
			return errors.New("unknown logical track")
		}
		kinds[id] = kind
		if kind == "audio" {
			keptTracks = append(keptTracks, tr)
		}
	}
	keptPackets := []map[string]json.RawMessage{}
	for _, p := range packets {
		id, kind, codec := str(p, "stream_id"), kinds[str(p, "ltid")], strings.ToLower(str(p, "codec"))
		if !validArtifactJob(id) || (kind != "audio" && kind != "video") {
			return errors.New("unknown packet stream")
		}
		if !strings.HasPrefix(codec, kind+"/") {
			return fmt.Errorf("packet codec contradicts track kind: %s", codec)
		}
		for _, ext := range []string{".rtplog", ".idx"} {
			f := filepath.Join(filepath.Dir(path), "streams", id+ext)
			if _, seen := known[f]; seen {
				return errors.New("duplicate packet stream")
			}
			known[f] = kind == "audio"
			if kind == "video" {
				if err = os.Remove(f); err != nil && !os.IsNotExist(err) {
					return err
				}
			} else {
				if _, err = os.Stat(f); err != nil {
					return err
				}
			}
		}
		if kind == "audio" {
			keptPackets = append(keptPackets, p)
		}
	}
	s["logical_tracks"], _ = json.Marshal(keptTracks)
	s["packet_streams"], _ = json.Marshal(keptPackets)
	if raw, ok := s["transceivers"]; ok {
		if err = json.Unmarshal(raw, &transceivers); err != nil {
			return err
		}
		kept := []map[string]json.RawMessage{}
		for _, tr := range transceivers {
			if str(tr, "kind") == "audio" {
				kept = append(kept, tr)
			} else if str(tr, "kind") != "video" {
				return errors.New("unknown transceiver kind")
			}
		}
		s["transceivers"], _ = json.Marshal(kept)
	}
	b, err = json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

// Only a private, unpublished staged tree is changed here. Unknown binary
// payloads fail closed rather than silently retaining unclassified video.
func stripCaptureVideo(ctx context.Context, root string) error {
	if _, err := requireReadyRunBundle(root); err != nil {
		return err
	}
	var paths []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in staged capture")
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range paths {
		if filepath.Base(p) == "session.json" {
			if err = stripSessionVideo(p, known); err != nil {
				return err
			}
		}
	}
	for _, p := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ext := strings.ToLower(filepath.Ext(p))
		switch ext {
		case ".json", ".ndjson", ".jsonl", ".log", ".txt":
			continue
		case ".rtplog", ".idx":
			if _, ok := known[p]; !ok {
				return fmt.Errorf("unclassified packet payload %s", filepath.Base(p))
			}
			continue
		case ".mkv", ".mka", ".webm", ".mp4", ".ogg", ".opus", ".wav", ".ivf", ".h264":
		default:
			return fmt.Errorf("unclassified capture payload %s", filepath.Base(p))
		}
		streams, err := probeCapture(ctx, p)
		if err != nil {
			return err
		}
		video, audio := false, false
		for _, s := range streams {
			switch s.Kind {
			case "video":
				video = true
			case "audio":
				audio = true
			default:
				return fmt.Errorf("unclassified container stream %s", s.Kind)
			}
		}
		if !video {
			continue
		}
		if !audio {
			if err = os.Remove(p); err != nil {
				return err
			}
			continue
		}
		before, err := fingerprintCaptureAudio(ctx, p, streams)
		if err != nil {
			return err
		}
		replacement := p + ".audio-only.mkv"
		cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-copyts", "-i", p, "-map", "0:a", "-c", "copy", "-map_metadata", "0", "-map_chapters", "0", "-avoid_negative_ts", "disabled", "-f", "matroska", replacement)
		if err = cmd.Run(); err != nil {
			return fmt.Errorf("audio-only remux: %w", err)
		}
		out, err := probeCapture(ctx, replacement)
		if err != nil {
			return err
		}
		for _, s := range out {
			if s.Kind != "audio" {
				return errors.New("video remains after remux")
			}
		}
		after, err := fingerprintCaptureAudio(ctx, replacement, out)
		if err != nil {
			return err
		}
		if len(before) != len(after) {
			return errors.New("audio stream count changed")
		}
		for i := range before {
			if before[i] != after[i] {
				return fmt.Errorf("audio packets, timestamps or metadata changed in stream %d", i)
			}
		}
		if err = os.Rename(replacement, p); err != nil {
			return err
		}
	}
	_, err = requireReadyRunBundle(root)
	return err
}

func (rt *Runtime) expireCaptureVideo(ctx context.Context, job Job, attempt int, p retentionPolicy, anchor, now time.Time, revision int) error {
	if !p.due(anchor, now) {
		return nil
	}
	source := canonicalRunPath(rt.cfg.WorkRoot, job.ID)
	if _, err := os.Stat(source); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var video string
	_ = rt.store.db.QueryRow(`SELECT video FROM artifact_availability WHERE job_id=?`, job.ID).Scan(&video)
	if video == "expired" {
		return nil
	}
	if rt.pendingArtifactOperation(job.ID) {
		return errors.New("pending capture operation")
	}
	if _, err := os.Lstat(attemptRunPath(rt.cfg.WorkRoot, job.ID, attempt)); err == nil {
		return errors.New("capture duplicate cleanup must finish before video expiry")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := validateArtifactTree(rt.cfg.WorkRoot, source); err != nil {
		return err
	}
	dir := rt.operationDir(job.ID)
	if err := validateArtifactTree(rt.cfg.WorkRoot, dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	staged := filepath.Join(dir, "new-0")
	defer func() {
		if !rt.pendingArtifactOperation(job.ID) {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := prepareAudioOnlyCapture(ctx, source, staged, copyDirectory); err != nil {
		return err
	}
	rel, _ := filepath.Rel(rt.cfg.WorkRoot, source)
	op := artifactOperation{Job: job.ID, Attempt: attempt, Action: "video", Kind: "video", Targets: []string{rel}, Revision: revision, Deadline: p.deadline(anchor).Format("2006-01-02")}
	return rt.startExpiryOperation(op)
}

func prepareAudioOnlyCapture(ctx context.Context, source, staged string, copyBundle func(string, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := copyBundle(source, staged); err != nil {
		return err
	}
	return stripCaptureVideo(ctx, staged)
}
