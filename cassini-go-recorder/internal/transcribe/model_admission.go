package transcribe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The file lock also serializes terminal imports/probes with operator builds.
// Downloading and audio-only processing never take this inference lock.
func lockModelRuntime(ctx context.Context, root string) (func(), error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, ".inference.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func probeMemoryFloor(model ModelID, device string) int {
	if device == "cuda" {
		return 6144
	}
	switch model {
	case ModelParakeet110M:
		return 1536
	case ModelParakeet06BV3Int8:
		return 2816
	default:
		return 4608
	}
}

// A local import is allowed to publish verified bytes on a small host, but must
// not mark them ready by starting a native probe without enough memory.
func admitModelProbe(ctx context.Context, model ModelID, device string) error {
	free := availableMemMB()
	limit, okL := readIntFile("/sys/fs/cgroup/memory.max")
	used, okU := readIntFile("/sys/fs/cgroup/memory.current")
	if okL && okU && limit > 0 {
		// Credit only clean inactive file cache, as the operator governor does.
		stat, _ := os.ReadFile("/sys/fs/cgroup/memory.stat")
		fields := map[string]int{}
		for _, line := range strings.Split(string(stat), "\n") {
			pair := strings.Fields(line)
			if len(pair) == 2 {
				fields[pair[0]], _ = strconv.Atoi(pair[1])
			}
		}
		clean := fields["inactive_file"]
		for _, key := range []string{"file_dirty", "file_writeback", "shmem"} {
			n, ok := fields[key]
			if !ok || n >= clean {
				clean = 0
				break
			}
			clean -= n
		}
		if clean > used {
			clean = used
		}
		cgFree := max(0, (limit-used+clean)/(1024*1024))
		if free <= 0 || cgFree < free {
			free = cgFree
		}
	}
	floor := probeMemoryFloor(model, device)
	if free < floor {
		return fmt.Errorf("model installed but runtime check needs %d MiB available RAM; have %d MiB; retry models probe when memory is available", floor, free)
	}
	if device == "cuda" {
		if os.Getenv("CASSINI_STT_CUDA_CAPABLE") != "1" {
			return fmt.Errorf("runtime check requires a CUDA-capable Cassini image (CASSINI_STT_CUDA_CAPABLE=1)")
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(probeCtx, "nvidia-smi", "--query-gpu=memory.free", "--format=csv,noheader,nounits").Output()
		if err != nil {
			return fmt.Errorf("cannot check CUDA free memory: %w", err)
		}
		// Sherpa uses the first visible GPU. CUDA_VISIBLE_DEVICES is honored by the
		// operator's admission too; conservatively require every listed GPU to fit.
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			n, e := strconv.Atoi(strings.TrimSpace(line))
			if e != nil || n < 5500 {
				return fmt.Errorf("runtime check requires at least 5500 MiB free GPU memory")
			}
		}
	}
	return nil
}
