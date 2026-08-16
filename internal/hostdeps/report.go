package hostdeps

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

func parsePackageReport(data []byte) (map[string]string, error) {
	versions := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid dpkg package report line %q", scanner.Text())
		}
		name := strings.TrimSpace(fields[0])
		status := fields[1]
		version := strings.TrimSpace(fields[2])
		if name == "" || status != "ii " || version == "" {
			return nil, fmt.Errorf("host package %q has status %q and version %q", name, status, version)
		}
		versions[name] = version
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dpkg package report: %w", err)
	}
	return versions, nil
}
