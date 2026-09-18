//go:build !linux && !darwin && !freebsd

package talk

import (
	"context"
	"fmt"
)

func acquireFinalizeLock(ctx context.Context, path string) (func(), error) {
	if path != "" {
		return nil, fmt.Errorf("shared recording finalization is unsupported on this platform")
	}
	return func() {}, nil
}
