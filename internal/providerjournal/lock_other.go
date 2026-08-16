//go:build !linux

package providerjournal

import (
	"context"
	"fmt"
)

type fileLock struct{}

func acquireFileLock(context.Context, string) (*fileLock, error) {
	return nil, fmt.Errorf("provider journal locking is supported only on Linux")
}

func (l *fileLock) Close() error { return nil }
