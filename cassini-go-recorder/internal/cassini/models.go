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
}

func runModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: cassini models list|install|probe|pack|import [model] [options]")
		return 2
	}
	command := args[0]
	fs := flag.NewFlagSet("cassini models "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("cache-root", defaultCassiniCacheRoot(), "persistent operator model cache root")
	revision := fs.String("revision", "", "pinned model revision (models list --json)")
	modelID := fs.String("model", "", "model ID, required for raw-file import")
	device := fs.String("device", "cpu", "target device: cpu or cuda")
	from := fs.String("from", "", "local pack, original tar.bz2, or extracted directory")
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
			if m.ID == "silero-vad" {
				continue
			}
			var dl, installed int64
			for _, c := range s.Components(m) {
				d, n := s.Catalogue.Sizes(c)
				dl += d
				installed += n
			}
			view := modelView{m, m.Revision, dl, installed, s.Complete(m) == nil, s.Ready(m, *device, transcribe.ModelRuntimeFingerprint()), *device}
			views = append(views, view)
		}
		if *jsonOutput {
			if err := enc.Encode(views); err != nil {
				return fail(err)
			}
		} else {
			for _, v := range views {
				fmt.Fprintf(stdout, "%s  %s  installed=%t ready=%t download=%d bytes\n", v.ID, v.Revision, v.Installed, v.Ready, v.DownloadBytes)
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
	if m.ID == "silero-vad" {
		return fail(fmt.Errorf("select a speech model; VAD is included automatically"))
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
		if err := transcribe.ProbeInstalledModel(ctx, s, m, *device); err != nil {
			return fail(fmt.Errorf("files installed, but runtime check failed: %w", err))
		}
		s.Progress(modelstore.Progress{Version: 1, Phase: "ready"})
	}
	if *jsonOutput {
		returnCode := enc.Encode(map[string]any{"model": m.ID, "revision": m.Revision, "ready": s.Ready(m, *device, transcribe.ModelRuntimeFingerprint())})
		if returnCode != nil {
			return fail(returnCode)
		}
	}
	return 0
}
