package cassini

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type serveOptions struct {
	inputPath string
	addr      string
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts := serveOptions{
		addr: "127.0.0.1:8765",
	}

	fs := flag.NewFlagSet("cassini serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.addr, "addr", "127.0.0.1:8765", "listen address")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage:
  cassini serve ./site
  cassini serve ./site --addr 127.0.0.1:8765

`+"\n")
		fs.PrintDefaults()
	}

	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		parseArgs = append(append([]string{}, args[1:]...), args[0])
	}

	if err := fs.Parse(parseArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	opts.inputPath = fs.Arg(0)
	if strings.TrimSpace(opts.addr) == "" {
		fmt.Fprintln(stderr, "serve configuration error: --addr must not be empty")
		return 2
	}

	root, summary, err := resolveServeRoot(opts.inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "serve failed: %v\n", err)
		return 1
	}

	listener, err := net.Listen("tcp", opts.addr)
	if err != nil {
		fmt.Fprintf(stderr, "serve failed: listen on %s: %v\n", opts.addr, err)
		return 1
	}
	defer func() {
		_ = listener.Close()
	}()

	server := &http.Server{
		Handler: http.FileServer(http.Dir(root)),
	}

	errCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	if err := waitForServeReady(listener.Addr().String(), errCh); err != nil {
		fmt.Fprintf(stderr, "serve failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "serving=%s\n", summary)
	fmt.Fprintf(stdout, "url -> http://%s/\n", listener.Addr().String())

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-errCh
		return 0
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(stderr, "serve failed: %v\n", err)
			return 1
		}
		return 0
	}
}

func resolveServeRoot(path string) (string, string, error) {
	if bundle, ok, err := LoadSiteBundle(path); err != nil {
		return "", "", err
	} else if ok {
		return bundle.RootDir, bundle.RootDir, nil
	}

	root, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve serve path: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", "", fmt.Errorf("stat serve path: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("serve path is not a directory: %s", root)
	}
	indexPath := filepath.Join(root, "index.html")
	if _, err := os.Stat(indexPath); err != nil {
		return "", "", fmt.Errorf("serve path does not contain index.html: %s", root)
	}
	return root, root, nil
}

func waitForServeReady(addr string, errCh <-chan error) error {
	client := &http.Client{Timeout: 200 * time.Millisecond}
	url := "http://" + addr + "/"
	deadline := time.Now().Add(2 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodHead, url, nil)
		if err != nil {
			return fmt.Errorf("prepare readiness request: %w", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		select {
		case serveErr := <-errCh:
			if serveErr != nil {
				return serveErr
			}
			return errors.New("server exited before becoming ready")
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("server did not become ready on %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
