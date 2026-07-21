package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func TestRecorderCreatesIsolatedArtifacts(t *testing.T) {
	recorder, err := New(t.TempDir(), "functional/inventory", "attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Record("create", "passed", map[string]any{"instance": "vm-1"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.WriteJSON("manifest.json", map[string]string{"run": "run-1"}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := os.ReadFile(filepath.Join(recorder.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"phase":"create"`) {
		t.Fatalf("events do not contain create phase: %s", events)
	}
	if _, err := os.Stat(filepath.Join(recorder.Dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWriteProtoJSON(t *testing.T) {
	recorder, err := New(t.TempDir(), "test/proto", "attempt-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	message, err := structpb.NewStruct(map[string]any{"package_name": "bash"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.WriteProtoJSON("inventory-full.json", message); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(recorder.Dir, "inventory-full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("proto artifact is empty or lacks newline: %q", data)
	}
}
