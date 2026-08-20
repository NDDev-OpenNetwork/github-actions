//go:build linux

package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

type placementLock struct{ file *os.File }

func acquirePlacementLock(ctx context.Context, path string) (*placementLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open placement lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &placementLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock placement: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("lock placement: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (l *placementLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock placement: %w", unlockErr)
	}
	return closeErr
}
