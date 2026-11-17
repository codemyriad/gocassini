package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	// envDiarize asks `cassini build` to separate speakers on the single-track
	// audio when the optional diarizer is installed ("auto"), or skips it ("off").
	envDiarize = "CASSINI_DIARIZE"
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
		"-show_entries", "stream=codec_name,sample_rate,channels,duration:stream_tags:format=duration:format_tags",
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
			CodecName  string            `json:"codec_name"`
			SampleRate string            `json:"sample_rate"`
			Channels   int               `json:"channels"`
			Duration   string            `json:"duration"`
			Tags       map[string]string `json:"tags"`
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
	if hasCassiniTags(probe.Streams[0].Tags) || hasCassiniTags(probe.Format.Tags) {
		return audioProbe{}, fmt.Errorf("the uploaded file is a Cassini portable meeting; import it as a meeting rather than audio")
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

func hasCassiniTags(tags map[string]string) bool {
	for k := range tags {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(k)), "CASSINI_") {
			return true
		}
	}
	return false
}

// remuxAudioToMKV turns any container ffmpeg reads into the MKV the build
// expects. It transcodes to FLAC rather than copying: a source codec Matroska
// cannot hold (or one this build cannot read) would otherwise fail halfway
// through an upload, and FLAC loses nothing before the meeting's own Opus
// encode. Source metadata is stripped (-map_metadata -1) so stream tags from
// an uploaded container never masquerade as Talk speaker labels or wall-clock
// anchors.
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
		"-map_metadata", "-1",
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

// buildAudioMeeting runs the ordinary `cassini build` over the prepared MKV,
// under the same admission as a Talk recording's build: the operator's build
// slot, the wait for recordings to idle, the speech model admission, the RAM
// floor and the device and thread limits. Like a Talk build it yields to a
// recording that needs the host and starts again once the host is free, and
// it waits out a shortage of memory or GPU instead of failing. Only at the
// retry ceiling, or when the model cannot run at all, does the upload become a
// meeting with audio and no transcript, and the meeting then says why.
func (imp *meetingImporter) buildAudioMeeting(ctx context.Context, mkv, out string, durationMS int64) error {
	if imp.sink == nil || strings.TrimSpace(imp.sink.cassiniBin) == "" {
		return fmt.Errorf("transcribe the uploaded audio: no cassini binary is configured")
	}
	rt := imp.sink.rt
	if rt == nil {
		// No runtime (unit tests and tools): nothing to admit against.
		env := setEnvKey(setEnvKey(contextChildEnv(os.Environ()), envDisallowModelDownload, "1"), envDiarize, "auto")
		return imp.runAudioBuild(ctx, env, mkv, out, durationMS, nil)
	}
	limit := rt.buildResourceDeferralLimit()
	for deferrals := 0; ; {
		err := imp.buildAudioMeetingOnce(ctx, rt, mkv, out, durationMS, deferrals >= limit)
		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, errRecordingPriority):
			// Yielding to a recording is not a resource failure and does not
			// count towards the ceiling. The next attempt waits for the host.
			rt.logger.Printf("audio import build yielded for recording out=%s", filepath.Base(out))
		case transientResourceError(err) && deferrals < limit:
			deferrals++
			delay := exponentialBuildRetryDelay(rt.buildResourceRetryDelay, deferrals)
			rt.logger.Printf("audio import build deferred count=%d retry_in=%s: %v", deferrals, delay, err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		default:
			return err
		}
		if err := clearPortableBuildOutput(out); err != nil {
			return fmt.Errorf("transcribe the uploaded audio: clear the interrupted build: %w", err)
		}
	}
}

// clearPortableBuildOutput removes both the target .opus file and the sibling
// .cassini-work directory `cassini build` stages intermediate artifacts in, so
// a yielded or crashed attempt cannot leave a partial bundle for the next run
// to adopt.
func clearPortableBuildOutput(out string) error {
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(filepath.Dir(out), ".cassini-work"))
}

// buildAudioMeetingOnce is one admitted attempt. atCeiling says waiting has
// run out, so a resource shortage keeps the audio rather than deferring.
func (imp *meetingImporter) buildAudioMeetingOnce(ctx context.Context, rt *Runtime, mkv, out string, durationMS int64, atCeiling bool) error {
	// The build gate is held for the whole build, as a Talk build holds it, so
	// two builds never both see the same RAM and VRAM as free. Wait for it in a
	// way a cancel can interrupt.
	for !rt.buildExecutionMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	defer rt.buildExecutionMu.Unlock()
	buildCtx, release, err := rt.beginBackgroundBuildUntil(ctx.Done())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer release()
	// The build's own context yields when a recording needs the host. Also
	// stop it when the person cancels the import.
	runCtx, cancelRun := context.WithCancel(buildCtx)
	defer cancelRun()
	stop := context.AfterFunc(ctx, cancelRun)
	defer stop()
	yielded := func(err error) error {
		if errors.Is(context.Cause(buildCtx), errRecordingPriority) {
			return errRecordingPriority
		}
		return err
	}

	limits := resourceLimitsFromEnv()
	settings := rt.currentSettings()
	device, model, mode, reason, diarize := deviceCPU, "", "off", "disabled", "off"
	keepAudio := func(cause error) {
		rt.logger.Printf("resource governor: audio import transcription unavailable, preserving audio: %v", cause)
		device, model, mode, reason, diarize = deviceCPU, "", "off", transcriptionReasonModelUnavailable, "off"
		if transientResourceError(cause) {
			reason = transcriptionReasonInsufficientResources
		}
	}
	if settings.TranscriptionEnabled {
		selectedDevice, admitted, err := rt.admitTranscription(runCtx, settings, limits, false)
		switch {
		case err == nil:
			settings = admitted
			device, model, mode, reason = selectedDevice, admitted.ActiveModel, "on", ""
			if ok, _ := rt.diarizationAvailability(runCtx); ok {
				diarize = "auto"
			}
			if runCtx.Err() != nil {
				return yielded(runCtx.Err())
			}
		case runCtx.Err() != nil:
			return yielded(runCtx.Err())
		case transientResourceError(err) && !atCeiling:
			return err
		default:
			keepAudio(err)
		}
	}
	needMem := limits.minFreeMemForBuild(device, model)
	diarizeMem := 0
	if diarize == "auto" {
		diarizeMem = speakerDiarizeMemMB(durationMS) + limits.cpuMemHeadroomMB
	}
	if err := limits.waitForMemory(runCtx, max(needMem, diarizeMem), rt.logger.Printf); err != nil {
		if runCtx.Err() != nil {
			return yielded(runCtx.Err())
		}
		if mode != "on" || !atCeiling || !transientResourceError(err) {
			return err
		}
		// At the retry ceiling, drop optional diarization before dropping
		// transcription when diarization is what raised the memory floor.
		if diarize == "auto" && diarizeMem > needMem {
			rt.logger.Printf("resource governor: audio import diarization skipped for memory, keeping transcription: %v", err)
			diarize = "off"
			err = limits.waitForMemory(runCtx, needMem, rt.logger.Printf)
			if runCtx.Err() != nil {
				return yielded(runCtx.Err())
			}
		}
		if err != nil {
			if !transientResourceError(err) {
				return err
			}
			keepAudio(err)
			if err := limits.waitForMemory(runCtx, limits.minFreeMemForBuild(device, model), rt.logger.Printf); err != nil {
				return yielded(err)
			}
		}
	}
	// The admission snapshot, even if settings changed during the waits, minus
	// the operator's own credentials (D-700: any signed-in user can upload audio).
	env := withoutEnv(settings.ChildEnv(rt.childEnv()), operatorOnlySecretEnv())
	env = setEnvKey(env, envCacheRoot, rt.cfg.ModelCacheRoot)
	env = setEnvKey(env, envDisallowModelDownload, "1")
	env = setEnvKey(env, "CASSINI_TRANSCRIPTION", mode)
	env = setEnvKey(env, "CASSINI_TRANSCRIPTION_REASON", reason)
	env = setEnvKey(env, envDiarize, diarize)
	env = setEnvKey(env, envDiarizationThreads, strconv.Itoa(limits.speakerDiarizeThreads()))
	buildEnv, err := limits.applyToEnv(env, device, model)
	if err != nil && mode == "on" && atCeiling && transientResourceError(err) {
		keepAudio(err)
		env = setEnvKey(setEnvKey(setEnvKey(env, "CASSINI_TRANSCRIPTION", mode), "CASSINI_TRANSCRIPTION_REASON", reason), envDiarize, diarize)
		buildEnv, err = limits.applyToEnv(env, device, model)
	}
	if err != nil {
		return err
	}
	rt.logger.Printf("resource governor: audio import transcription=%s diarize=%s device=%s model=%s reason=%s", mode, diarize, device, model, reason)
	var fallbackEnv []string
	if mode == "on" {
		// A fatal native inference error (CUDA out of memory, a cuDNN failure)
		// can take the build down; the audio is still worth a meeting.
		fallbackEnv = setEnvKey(setEnvKey(setEnvKey(buildEnv, "CASSINI_TRANSCRIPTION", "off"), "CASSINI_TRANSCRIPTION_REASON", transcriptionReasonFailed), envDiarize, "off")
	}
	return yielded(imp.runAudioBuild(runCtx, buildEnv, mkv, out, durationMS, fallbackEnv))
}

// runAudioBuild runs `cassini build` once, and once more without transcription
// when fallbackEnv is set and the first run failed on its own (not because it
// was stopped).
func (imp *meetingImporter) runAudioBuild(ctx context.Context, env []string, mkv, out string, durationMS int64, fallbackEnv []string) error {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeoutForDuration(durationMS, 30*time.Minute, 6*time.Hour))
	defer cancel()
	run := func(env []string, extra ...string) (string, error) {
		args := append([]string{"build", mkv, "--out", out}, extra...)
		cmd := cassiniCommandEnv(ctx, imp.sink.cassiniBin, env, args)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := cmd.Run()
		return output.String(), err
	}
	output, err := run(env)
	if err != nil && fallbackEnv != nil && ctx.Err() == nil {
		if cleanErr := clearPortableBuildOutput(out); cleanErr != nil {
			return fmt.Errorf("transcribe the uploaded audio: %v: %s", err, tailOfOutput(output))
		}
		firstErr, firstOutput := err, output
		output, err = run(fallbackEnv, "--transcription", "off")
		if err != nil {
			return fmt.Errorf("transcribe the uploaded audio: %v: %s", firstErr, tailOfOutput(firstOutput))
		}
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("transcribe the uploaded audio: the build ran past its time limit: %s", tailOfOutput(output))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("transcribe the uploaded audio: %v: %s", err, tailOfOutput(output))
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
