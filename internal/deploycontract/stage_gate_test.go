package deploycontract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
)

// The exporter refuses to start outside its accepted deployment stages, and the
// observer refuses to grade a status reported from outside them. Those were two
// independent string literals, so promoting the exporter would have relaxed its
// own validator while leaving the observer refusing the very status the
// promotion produces -- a fail-closed observer on every serving host, caused by
// a successful promotion.
//
// They are now one declaration. This test exists so they stay one: it fails if a
// stage name reappears as a literal anywhere outside the package that declares
// it, which is exactly how the split happened the first time.
func TestAcceptedStagesAreTheOnlyStageGate(t *testing.T) {
	stages := diagnosticexport.AcceptedStages()
	if len(stages) == 0 {
		t.Fatal("the accepted stage set must not be empty")
	}
	wanted := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		wanted[stage] = struct{}{}
	}

	// telemetrymanifest describes the collector and the object store, which are
	// separate components with their own promotion state, so its stage literals
	// are not this gate and are deliberately out of scope.
	skipped := map[string]struct{}{
		filepath.Join("..", "diagnosticexport"):  {},
		filepath.Join("..", "telemetrymanifest"): {},
	}

	root := filepath.Join("..")
	fileSet := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if _, skip := skipped[path]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			if _, forbidden := wanted[value]; forbidden {
				t.Errorf(
					"%s hardcodes deployment stage %q; read diagnosticexport.StageAccepted instead",
					fileSet.Position(literal.Pos()), value,
				)
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}
