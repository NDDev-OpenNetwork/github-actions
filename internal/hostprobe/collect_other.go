//go:build !linux

package hostprobe

import (
	"context"
	"fmt"
)

func Collect(context.Context) (Snapshot, error) {
	return Snapshot{}, fmt.Errorf("host preflight requires Linux")
}
