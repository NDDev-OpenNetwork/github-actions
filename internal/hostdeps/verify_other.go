//go:build !linux

package hostdeps

import (
	"context"
	"fmt"
)

func VerifyIncusVM(context.Context) (map[string]string, error) {
	return nil, fmt.Errorf("Incus VM host dependency verification is supported only on Linux")
}

func VerifyIncusContainer(context.Context) (map[string]string, error) {
	return nil, errors.New("Incus container host dependency verification requires Linux")
}
