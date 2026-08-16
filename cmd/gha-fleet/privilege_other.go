//go:build !linux

package main

import "fmt"

func requireLinuxRoot() error {
	return fmt.Errorf("--apply is supported only on the Linux Incus host")
}

func requireCredentialRoot() error {
	return fmt.Errorf("--apply is supported only on the Linux credential host")
}
