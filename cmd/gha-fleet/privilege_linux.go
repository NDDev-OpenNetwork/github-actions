//go:build linux

package main

import (
	"fmt"
	"os"
)

func requireLinuxRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("--apply requires root and the local Incus Unix socket")
	}
	return nil
}

func requireCredentialRoot() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("--apply requires root to write the private credential directory")
	}
	return nil
}
