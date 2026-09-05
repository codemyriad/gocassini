package transcribe

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A conservative admission check, not a reservation. Inference remains opt-in
// and independent processes can consume memory after this snapshot.
func checkSummaryMemory(cfg LLMConfig) error {
	available := procMemAvailableMB()
	known := available > 0
	path := "/sys/fs/cgroup"
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "0::/") {
				path = filepath.Join(path, strings.TrimPrefix(line, "0::/"))
				break
			}
		}
	}
	for {
		limit, e1 := os.ReadFile(filepath.Join(path, "memory.max"))
		current, e2 := os.ReadFile(filepath.Join(path, "memory.current"))
		stat, _ := os.ReadFile(filepath.Join(path, "memory.stat"))
		if e1 == nil && e2 == nil {
			if free, ok := summaryCgroupFreeMB(string(limit), string(current), string(stat)); ok && (!known || free < available) {
				available = free
				known = true
			}
		}
		if path == "/sys/fs/cgroup" || !strings.HasPrefix(path, "/sys/fs/cgroup/") {
			break
		}
		path = filepath.Dir(path)
	}
	needed := 6656 + cfg.ContextSize/32 // ~7 GiB at the initial 16K CPU context
	if cfg.Device == "cuda" {
		// GPU weights live in VRAM. Loading touches mmap-backed host pages,
		// but those file pages are reclaimable; reserve host working buffers.
		needed = 2048 + cfg.ContextSize/32
	}
	if known && available < needed {
		return fmt.Errorf("local summary needs approximately %d MiB available RAM; only %d MiB available; skipping summary", needed, available)
	}
	return nil
}

func summaryCgroupFreeMB(limit, current, stat string) (int, bool) {
	max, e1 := strconv.ParseInt(strings.TrimSpace(limit), 10, 64)
	used, e2 := strconv.ParseInt(strings.TrimSpace(current), 10, 64)
	if e1 != nil || e2 != nil || max <= 0 || used < 0 {
		return 0, false
	}
	var reclaim int64
	for _, line := range strings.Split(stat, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "inactive_file" {
			reclaim, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	if reclaim < 0 {
		reclaim = 0
	}
	if reclaim > used {
		reclaim = used
	}
	free := max - used + reclaim
	if free < 0 {
		free = 0
	}
	return int(free / (1 << 20)), true
}
