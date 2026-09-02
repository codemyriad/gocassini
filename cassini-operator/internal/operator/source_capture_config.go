package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The companion PHP app cannot read an ExApp container environment variable.
// AppAPI exposes a public, ExApp-authenticated app-config endpoint for exactly
// this bridge. It stores the value under gocassini in Nextcloud's IAppConfig;
// cassini_capture reads it while building the call page's initial state.
const (
	sourceCaptureAppConfigKey = "source_capture_enabled"
	appAPIAppConfigPath       = "/ocs/v2.php/apps/app_api/api/v1/ex-app/config"
)

// captureConfigSyncTimeout bounds the companion-state write. It is held open
// across an AppAPI lifecycle request on the disable edge, so it must be short
// enough that a slow Nextcloud cannot stall disabling the app.
const captureConfigSyncTimeout = 5 * time.Second

func (c ExAppConfig) syncSourceCaptureInitialState(ctx context.Context, enabled bool, logger *log.Logger) error {
	if !c.appAPIActive() {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"configKey":   sourceCaptureAppConfigKey,
		"configValue": strconv.FormatBool(enabled),
		"sensitive":   0,
	})
	if err != nil {
		return fmt.Errorf("encode source-capture app config: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(c.NextcloudURL, "/")+appAPIAppConfigPath,
		bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("build source-capture app-config request: %w", err)
	}
	c.setAppAPIOCSHeaders(req)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("write source-capture app config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("write source-capture app config -> %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if logger != nil {
		logger.Printf("source capture: synchronized companion initial state enabled=%t", enabled)
	}
	return nil
}
