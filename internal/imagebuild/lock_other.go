//go:build !linux

package imagebuild

import "fmt"

type Lock struct{}

func AcquireLock(path string) (*Lock, error) {
	return nil, fmt.Errorf("image builds are supported only on Linux")
}

func (l *Lock) Close() error { return nil }
