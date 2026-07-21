package artifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var unsafePath = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Recorder owns a unique artifact directory for one test attempt.
type Recorder struct {
	Dir    string
	events *os.File
	mu     sync.Mutex
}

// Event is one structured lifecycle observation.
type Event struct {
	Time   time.Time      `json:"time"`
	Phase  string         `json:"phase"`
	Status string         `json:"status"`
	Fields map[string]any `json:"fields,omitempty"`
}

// New creates an isolated test-attempt artifact directory.
func New(root, testID, attemptID string) (*Recorder, error) {
	dir := filepath.Join(root, safe(testID), safe(attemptID))
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return nil, fmt.Errorf("create artifact directory %q: %w", dir, err)
	}
	events, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o660)
	if err != nil {
		return nil, fmt.Errorf("create events file: %w", err)
	}
	return &Recorder{Dir: dir, events: events}, nil
}

// Record writes one JSON-lines event.
func (r *Recorder) Record(phase, status string, fields map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return json.NewEncoder(r.events).Encode(Event{Time: time.Now().UTC(), Phase: phase, Status: status, Fields: fields})
}

// WriteJSON atomically writes a formatted JSON artifact.
func (r *Recorder) WriteJSON(name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	return r.writeAtomic(name, append(data, '\n'))
}

// WriteProtoJSON atomically writes a protobuf with stable proto field names.
func (r *Recorder) WriteProtoJSON(name string, value proto.Message) error {
	data, err := (protojson.MarshalOptions{Multiline: true, Indent: "  ", UseProtoNames: true}).Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	return r.writeAtomic(name, append(data, '\n'))
}

func (r *Recorder) writeAtomic(name string, data []byte) error {
	temporary := filepath.Join(r.Dir, safe(name)+".tmp")
	final := filepath.Join(r.Dir, safe(name))
	if err := os.WriteFile(temporary, data, 0o660); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(temporary, final); err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	return nil
}

// WriteText writes a text artifact.
func (r *Recorder) WriteText(name, value string) error {
	return os.WriteFile(filepath.Join(r.Dir, safe(name)), []byte(value), 0o660)
}

// Close flushes the event stream.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events.Close()
}

func safe(value string) string {
	value = unsafePath.ReplaceAllString(value, "-")
	if value == "" {
		return "unnamed"
	}
	return value
}
