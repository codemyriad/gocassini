package cassini

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gocassini/internal/modelstore"
	"gocassini/internal/transcribe"
)

type modelView struct {
	modelstore.Model
	Revision       string `json:"revision"`
	DownloadBytes  int64  `json:"download_bytes"`
	InstalledBytes int64  `json:"installed_bytes"`
	Installed      bool   `json:"installed"`
	Ready          bool   `json:"ready"`
	Device         string `json:"device"`
	// RuntimeSupported is set for diarization models only: whether this
	// binary's native runtime can run them at all.
	RuntimeSupported *bool `json:"runtime_supported,omitempty"`
}

// listModelView is one `models list` row. A speech model counts its VAD in
// the sizes and is ready once it passed the runtime check on the device. A
// diarizer always runs on the CPU, has nothing to pair with, and is ready when
// it is installed and the runtime supports Nemotron: that is cheap to check
// on every listing and, unlike a receipt, survives a reboot.
func listModelView(s *modelstore.Store, m modelstore.Model, device string) modelView {
	var dl, installed int64
	for _, c := range s.Components(m) {
		d, n := s.Catalogue.Sizes(c)
		dl += d
		installed += n
	}
	view := modelView{Model: m, Revision: m.Revision, DownloadBytes: dl, InstalledBytes: installed, Installed: s.Complete(m) == nil, Device: device}
	if m.Kind == modelstore.KindDiarization {
		supported := diarizationRuntimeFn()
		view.RuntimeSupported = &supported
		view.Device = "cpu"
		view.Ready = view.Installed && supported
		return view
	}
	view.Ready = s.Ready(m, device, transcribe.ModelRuntimeFingerprint())
	return view
}

// Seams so the command can be tested without the native runtime.
var (
	diarizationRuntimeFn   = transcribe.HasDiarizationRuntime
	probeInstalledModelFn  = transcribe.ProbeInstalledModel
	probeInstalledDiarizer = transcribe.ProbeInstalledDiarizer
)

func runModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: cassini models list|install|probe|pack|import [model] [options]")
		fmt.Fprintln(stderr, "Models are speech models (installed with their VAD) and the speaker separation model "+transcribe.DefaultDiarizationModelID+".")
		return 2
	}
	command := args[0]
	fs := flag.NewFlagSet("cassini models "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("cache-root", defaultCassiniCacheRoot(), "persistent operator model cache root")
	revision := fs.String("revision", "", "pinned model revision (models list --json)")
	modelID := fs.String("model", "", "model ID, required for raw-file import")
	device := fs.String("device", "cpu", "target device: cpu or cuda")
	from := fs.String("from", "", "local pack, original tar.bz2, extracted directory, or a single-file model's file (.zst or decompressed)")
	vad := fs.String("vad", "", "local Silero VAD file (or .zst), for raw import")
	out := fs.String("out", "", "output model pack path")
	jsonOutput := fs.Bool("json", false, "machine-readable inventory/result")
	progressJSON := fs.Bool("progress-json", false, "versioned JSON progress events")
	noProbe := fs.Bool("no-probe", false, "install bytes only; readiness requires a later models probe")
	parse := args[1:]
	if len(parse) > 0 && !strings.HasPrefix(parse[0], "-") {
		parse = append(append([]string{}, parse[1:]...), parse[0])
	}
	if err := fs.Parse(parse); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "expected at most one model ID")
		return 2
	}
	if fs.NArg() == 1 {
		if *modelID != "" {
			fmt.Fprintln(stderr, "specify the model only once")
			return 2
		}
		*modelID = fs.Arg(0)
	}
	s := modelstore.New(*root)
	switch strings.ToLower(os.Getenv("CASSINI_DISALLOW_MODEL_DOWNLOAD")) {
	case "1", "true", "yes":
		s.NoDownload = true
	}
	enc := json.NewEncoder(stdout)
	s.Progress = func(p modelstore.Progress) {
		if *progressJSON {
			_ = enc.Encode(p)
		} else {
			fmt.Fprintf(stderr, "%s %s: %d / %d bytes\n", p.Phase, p.File, p.Completed, p.Total)
		}
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "models:", err); return 1 }
	if err := s.Catalogue.Validate(); err != nil {
		return fail(err)
	}
	if *device != "cpu" && *device != "cuda" {
		return fail(fmt.Errorf("device must be cpu or cuda"))
	}
	if command == "list" {
		views := []modelView{}
		for _, m := range s.Catalogue.Models {
			if m.Kind == modelstore.KindVAD {
				continue
			}
			views = append(views, listModelView(s, m, *device))
		}
		if *jsonOutput {
			if err := enc.Encode(views); err != nil {
				return fail(err)
			}
		} else {
			for _, v := range views {
				line := fmt.Sprintf("%s  %s  %s  installed=%t ready=%t download=%d bytes", v.ID, v.Kind, v.Revision, v.Installed, v.Ready, v.DownloadBytes)
				if v.RuntimeSupported != nil && !*v.RuntimeSupported {
					line += "  (this runtime cannot run it)"
				}
				fmt.Fprintln(stdout, line)
			}
		}
		return 0
	}
	isPack := false
	if command == "import" {
		if *from == "" {
			return fail(fmt.Errorf("--from is required"))
		}
		if p, err := modelstore.PackIdentity(*from); err == nil {
			if (*modelID != "" && *modelID != p.Model) || (*revision != "" && *revision != p.Revision) {
				return fail(fmt.Errorf("explicit model/revision does not match package"))
			}
			*modelID = p.Model
			*revision = p.Revision
			isPack = true
		} else if *modelID == "" || *revision == "" {
			return fail(fmt.Errorf("raw-file import requires --model and --revision; package: %w", err))
		}
	}
	m, err := s.Catalogue.Model(*modelID, *revision)
	if err != nil {
		return fail(err)
	}
	if m.Kind == modelstore.KindVAD {
		return fail(fmt.Errorf("select a speech or diarization model; VAD is included with speech models automatically"))
	}
	diarizer := m.Kind == modelstore.KindDiarization
	if diarizer && *device != "cpu" && command != "pack" {
		return fail(fmt.Errorf("%s runs on the CPU only; omit --device", m.ID))
	}
	switch command {
	case "pack":
		if *out == "" {
			return fail(fmt.Errorf("--out is required"))
		}
		if err := s.Pack(ctx, m, *out); err != nil {
			return fail(err)
		}
		return 0
	case "import":
		err = s.Import(ctx, m, modelstore.ImportOptions{From: *from, VAD: *vad, Pack: isPack})
	case "install":
		err = s.Acquire(ctx, m)
	case "probe":
		err = s.Complete(m)
	default:
		return fail(fmt.Errorf("unknown models command %q", command))
	}
	if err != nil {
		return fail(err)
	}
	if !*noProbe {
		s.Progress(modelstore.Progress{Version: 1, Phase: "checking"})
		if diarizer {
			err = probeInstalledDiarizer(ctx, s, m)
		} else {
			err = probeInstalledModelFn(ctx, s, m, *device)
		}
		if err != nil {
			if errors.Is(err, transcribe.ErrRuntimeCheckDeferred) {
				fmt.Fprintln(stderr, "models:", err)
				return exitTempFail
			}
			return fail(fmt.Errorf("files installed, but runtime check failed: %w", err))
		}
		s.Progress(modelstore.Progress{Version: 1, Phase: "ready"})
	}
	if *jsonOutput {
		returnCode := enc.Encode(map[string]any{"model": m.ID, "revision": m.Revision, "ready": listModelView(s, m, *device).Ready})
		if returnCode != nil {
			return fail(returnCode)
		}
	}
	return 0
}

// exitTempFail (EX_TEMPFAIL) tells the operator a runtime check was refused for
// lack of memory and can be retried later.
const exitTempFail = 75
