package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDocsUpToDate checks that the generated documentation matches the
// command definitions: run `make docs` after changing them.
func TestDocsUpToDate(t *testing.T) {
	app := NewApp("")
	for name, write := range docFiles {
		var want bytes.Buffer
		if err := write(app, &want); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("..", "..", "documentation", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want.Bytes()) {
			t.Errorf("documentation/%s is out of date: run `make docs`", name)
		}
	}
}
