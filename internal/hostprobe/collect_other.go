//go:build !linux

package hostprobe

import (
	"context"
	"fmt"
)

func Collect(context.Context) (Snapshot, error) {
	return Snapshot{}, fmt.Errorf("host preflight requires Linux")
}

func ReadPressure(string) (Pressure, error) {
	return Pressure{}, fmt.Errorf("pressure stall information requires Linux")
}
