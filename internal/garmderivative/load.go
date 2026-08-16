package garmderivative

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// DefaultManifestPath is where the manifest lives relative to the repository
// root. The build script and the operator command both resolve it from there.
const DefaultManifestPath = "config/garm-derivative.yaml"

const maxManifestBytes = 256 * 1024

func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open GARM derivative manifest: %w", err)
	}
	defer file.Close()

	manifest, err := Decode(file)
	if err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return manifest, nil
}

func Decode(reader io.Reader) (Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read GARM derivative manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("GARM derivative manifest exceeds %d bytes", maxManifestBytes)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("parse trailing YAML: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
