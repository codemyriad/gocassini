package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Audio meeting upload (the alternate creation path): a raw recording is
// remuxed into the one-track MKV the build pipeline reads, transcribed and
// summarised exactly as a Talk capture is, then delivered through the same
// import engine — bytes, catalog entry, shares, retention and search.
//
// It is deliberately the same engine rather than a second publication path:
// the only new work is turning arbitrary audio into the build's input and
// letting the existing build produce the meeting that the import engine
// already knows how to validate and publish.

const (
	// Uploaded audio is its own catalog bucket. It was not imported from
	// another Cassini (the Imported meetings bucket), so filing it there would
	// say something untrue about where the meeting came from.
	uploadedRoomID   = "uploaded"
	uploadedRoomName = "Uploaded meetings"

	// maxAudioTitleBytes bounds the title read from a source filename so the
	// built output's name stays inside filesystem limits. Counted in bytes, not
	// runes: a name of 120 four-byte runes would not fit a 255-byte filename.
	maxAudioTitleBytes = 160
)

// audioProbe is what ffprobe says about the uploaded recording. Only the first
// audio stream is used; a file with several tracks is mixed by the build after
// this one track is selected, which is the same path a caller-supplied MKV
// takes.
type audioProbe struct {
	DurationMS int64
	Codec      string
	Channels   int
	SampleRate int
	Title      string
}

// audioTitleFromSource is the meeting title an upload gets when the client
// supplies no other: the source filename without its extension. It is a
// display title, not a path, so it keeps spaces and punctuation.
func audioTitleFromSource(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(base, filepath.Ext(base)))
}

// sanitizeAudioTitle makes a display title usable as the build's output
// filename. It keeps the words as they were, drops path and control characters,
// and never yields "." or "..".
func sanitizeAudioTitle(title string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.TrimSpace(title) {
		switch {
		case r == '/' || r == '\\' || r == 0 || unicode.IsControl(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = unicode.IsSpace(r)
		}
	}
	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ". ")
	if len(out) > maxAudioTitleBytes {
		var clipped strings.Builder
		for _, r := range out {
			if clipped.Len()+utf8.RuneLen(r) > maxAudioTitleBytes {
				break
			}
			clipped.WriteRune(r)
		}
		out = strings.TrimSpace(clipped.String())
	}
	if out == "" || out == "." || out == ".." {
		return "Uploaded meeting"
	}
	return out
}

// probeAudioSource validates that the upload has a readable first audio
// stream. The build would fail later without this, but a precise refusal here
// is what a person can act on.
func probeAudioSource(ctx context.Context, path string) (audioProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,channels,duration:format=duration:format_tags=title",
		"-of", "json",
		path,
	)
	cmd.Env = contextChildEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return audioProbe{}, fmt.Errorf("the uploaded file is not readable audio: %v: %s", err, tailOfOutput(stderr.String()))
	}
	var probe struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
			Duration   string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return audioProbe{}, fmt.Errorf("read audio metadata: %w", err)
	}
	if len(probe.Streams) == 0 {
		return audioProbe{}, fmt.Errorf("the uploaded file has no audio stream")
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
	if err != nil || duration <= 0 {
		// Some containers leave the format duration blank but carry one on the
		// stream; either is enough to bound processing.
		duration, err = strconv.ParseFloat(strings.TrimSpace(probe.Streams[0].Duration), 64)
	}
	if err != nil || duration <= 0 {
		return audioProbe{}, fmt.Errorf("the uploaded file reports no playable duration")
	}
	sampleRate, _ := strconv.Atoi(probe.Streams[0].SampleRate)
	return audioProbe{
		DurationMS: int64(duration * 1000),
		Codec:      probe.Streams[0].CodecName,
		Channels:   probe.Streams[0].Channels,
		SampleRate: sampleRate,
		Title:      strings.TrimSpace(probe.Format.Tags["title"]),
	}, nil
}

// remuxAudioToMKV turns any container ffmpeg reads into the MKV the build
// expects. It transcodes to FLAC rather than copying: a source codec Matroska
// cannot hold (or one this build cannot read) would otherwise fail halfway
// through an upload, and FLAC loses nothing before the meeting's own Opus
// encode.
func (imp *meetingImporter) remuxAudioToMKV(ctx context.Context, src, dir, title string) (string, audioProbe, error) {
	probe, err := probeAudioSource(ctx, src)
	if err != nil {
		return "", audioProbe{}, err
	}
	out := filepath.Join(dir, "audio.mkv")
	timeout := mediaTimeoutForDuration(probe.DurationMS, 10*time.Minute, 2*time.Hour)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-v", "error", "-y",
		"-i", src,
		"-map", "0:a:0", "-vn",
		"-c:a", "flac", "-compression_level", "0",
		"-metadata", "title="+title,
		"-f", "matroska", out,
	)
	cmd.Env = contextChildEnv(os.Environ())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	if err := cmd.Run(); err != nil {
		return "", audioProbe{}, fmt.Errorf("prepare the uploaded audio: %v: %s", err, tailOfOutput(stderr.String()))
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() <= 0 {
		return "", audioProbe{}, fmt.Errorf("prepare the uploaded audio: ffmpeg wrote no recording")
	}
	return out, probe, nil
}

// mediaTimeoutForDuration bounds a media step by the meeting's length, with a
// floor for setup and a ceiling so a wedged child cannot run forever.
func mediaTimeoutForDuration(durationMS int64, floor, ceiling time.Duration) time.Duration {
	scaled := time.Duration(durationMS) * time.Millisecond * 2
	timeout := scaled + floor
	if timeout > ceiling {
		timeout = ceiling
	}
	return timeout
}

// buildAudioMeeting runs the ordinary `cassini build` over the prepared MKV.
// It takes the operator's build slot and waits for recordings to idle, so an
// upload never competes with capture; the operator's own environment carries
// the configured speech model and summary endpoint, which is what makes the
// result a normal Cassini meeting rather than audio with no transcript.
func (imp *meetingImporter) buildAudioMeeting(ctx context.Context, mkv, out string, durationMS int64) error {
	timeout := mediaTimeoutForDuration(durationMS, 30*time.Minute, 6*time.Hour)
	if imp.sink == nil || strings.TrimSpace(imp.sink.cassiniBin) == "" {
		return fmt.Errorf("transcribe the uploaded audio: no cassini binary is configured")
	}
	env := contextChildEnv(os.Environ())
	var rt *Runtime
	if imp.sink != nil {
		rt = imp.sink.rt
	}
	if rt != nil {
		rt.buildExecutionMu.Lock()
		defer rt.buildExecutionMu.Unlock()
		buildCtx, release, err := rt.beginBackgroundBuild()
		if err != nil {
			return err
		}
		defer release()
		// The build's own context yields when a recording needs the host. Also
		// stop it when the person cancels the import: both reasons are honest.
		runCtx, cancelRun := context.WithCancel(buildCtx)
		defer cancelRun()
		go func() {
			select {
			case <-ctx.Done():
				cancelRun()
			case <-runCtx.Done():
			}
		}()
		ctx = runCtx
		env = rt.childEnv()
	}
	env = setEnvKey(env, envDisallowModelDownload, "1")
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := cassiniCommandEnv(ctx, imp.sink.cassiniBin, env, []string{"build", mkv, "--out", out})
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("transcribe the uploaded audio: %v: %s", err, tailOfOutput(output.String()))
	}
	if info, err := os.Stat(out); err != nil || info.Size() <= 0 {
		return fmt.Errorf("transcribe the uploaded audio: the build wrote no meeting file")
	}
	return nil
}

// runAudio is the audio sibling of meetingImporter.run: prepare, build, then
// validate and deliver exactly like a portable meeting.
func (imp *meetingImporter) runAudio(ctx context.Context, req meetingImportRequest, report importPhaseFunc) (meetingImportResult, error) {
	if report == nil {
		report = func(string, string) {}
	}
	if req.ImportID == "" || req.CreatedBy == "" || req.LocalPath == "" {
		return meetingImportResult{}, fmt.Errorf("invalid audio import request")
	}
	report(importStateValidating, "transcribing")
	staging, err := os.MkdirTemp(filepath.Dir(req.LocalPath), ".audio-"+req.ImportID+"-")
	if err != nil {
		return meetingImportResult{}, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	title := sanitizeAudioTitle(req.Title)
	if strings.TrimSpace(req.Title) == "" {
		title = sanitizeAudioTitle(audioTitleFromSource(req.LocalPath))
	}
	mkv, probe, err := imp.remuxAudioToMKV(ctx, req.LocalPath, staging, title)
	if err != nil {
		return meetingImportResult{}, err
	}
	built := filepath.Join(staging, title+".opus")
	if err := imp.buildAudioMeeting(ctx, mkv, built, probe.DurationMS); err != nil {
		return meetingImportResult{}, err
	}
	report(importStateValidating, "validating")
	req.LocalPath = built
	req.Title = title
	req.RoomID, req.RoomName = uploadedRoomID, uploadedRoomName
	req.Provenance = "uploaded"
	return imp.run(ctx, req, report)
}
