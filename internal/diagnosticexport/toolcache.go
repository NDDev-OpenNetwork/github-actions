package diagnosticexport

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const (
	toolCacheMarker    = "nddev_tool_cache_event="
	maxToolCacheEvents = 64
	maxToolCacheLine   = 16 * 1024
)

type ToolCacheEvent struct {
	Source      string `json:"source"`
	CacheResult string `json:"cache_result"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	DurationMS  int64  `json:"duration_ms"`
}

func ExtractToolCacheEvents(content []byte) ([]ToolCacheEvent, int) {
	compressed := bytes.NewReader(content)
	decompressor, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, 1
	}
	defer decompressor.Close()
	archive := tar.NewReader(decompressor)
	events := make([]ToolCacheEvent, 0)
	rejected := 0
	for len(events) < maxToolCacheEvents {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return events, rejected + 1
		}
		if header.Typeflag != tar.TypeReg || !strings.HasPrefix(header.Name, "runner/") {
			continue
		}
		scanner := bufio.NewScanner(io.LimitReader(archive, 2*1024*1024))
		scanner.Buffer(make([]byte, 4096), maxToolCacheLine)
		for scanner.Scan() && len(events) < maxToolCacheEvents {
			line := scanner.Text()
			index := strings.Index(line, toolCacheMarker)
			if index < 0 {
				continue
			}
			var event ToolCacheEvent
			decoder := json.NewDecoder(strings.NewReader(line[index+len(toolCacheMarker):]))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&event); err != nil || !validToolCacheEvent(event) {
				rejected++
				continue
			}
			events = append(events, event)
		}
		if scanner.Err() != nil {
			rejected++
		}
	}
	return events, rejected
}

func validToolCacheEvent(event ToolCacheEvent) bool {
	if event.Source == "" || len(event.Source) > 32 || event.CacheResult == "" || len(event.CacheResult) > 96 ||
		event.Bytes < 0 || event.DurationMS < 0 || len(event.SHA256) != 64 {
		return false
	}
	_, err := hex.DecodeString(event.SHA256)
	return err == nil
}
