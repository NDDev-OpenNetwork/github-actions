//go:build !linux

package provider

import (
	"context"
	"fmt"
)

type placementLock struct{}

func acquirePlacementLock(context.Context, string) (*placementLock, error) {
	return nil, fmt.Errorf("placement locking is supported only on Linux")
}

func (l *placementLock) Close() error { return nil }
