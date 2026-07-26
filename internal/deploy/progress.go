// Package deploy provisions an OpenFrp server over SSH.
//
// The engine lives in the client binary rather than in a shell script for
// three reasons that all showed up in practice: it emits structured progress
// instead of scraped stderr, it controls host-key verification itself, and it
// does not depend on which options the local SSH client happened to be built
// with. On the development machine `sshpass` silently failed to deliver a
// password to one of the test servers — burning real authentication attempts
// and looking exactly like a wrong credential — while this path authenticated
// first try.
package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Level classifies a progress event.
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarn    Level = "warn"
	LevelError   Level = "error"
	LevelSuccess Level = "success"
)

// Event is one line of deployment progress.
//
// Emitted as line-delimited JSON so the LuCI job worker can stream it straight
// to a log file and the UI can render it incrementally without waiting for the
// deployment to finish.
type Event struct {
	Time    string `json:"time"`
	Step    string `json:"step"`
	Level   Level  `json:"level"`
	Message string `json:"message"`
	// Detail carries structured findings — detected distribution, chosen init
	// system — so the UI can present them without parsing prose.
	Detail map[string]string `json:"detail,omitempty"`
}

// Reporter emits progress events.
type Reporter interface {
	Emit(Event)
}

// JSONReporter writes line-delimited JSON.
type JSONReporter struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// NewJSONReporter writes events to w.
func NewJSONReporter(w io.Writer) *JSONReporter {
	return &JSONReporter{w: w, now: time.Now}
}

// Emit writes one event. Failures are dropped: a deployment must not abort
// because its log pipe closed.
func (r *JSONReporter) Emit(e Event) {
	if e.Time == "" {
		e.Time = r.now().UTC().Format(time.RFC3339)
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}

	payload, err := json.Marshal(e)
	if err != nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	fmt.Fprintf(r.w, "%s\n", payload)
}

// TextReporter writes human-readable lines, for running deploy from a shell.
type TextReporter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewTextReporter writes plain text to w.
func NewTextReporter(w io.Writer) *TextReporter {
	return &TextReporter{w: w}
}

// Emit writes one line.
func (r *TextReporter) Emit(e Event) {
	marker := map[Level]string{
		LevelInfo:    "  ",
		LevelWarn:    " !",
		LevelError:   " ✗",
		LevelSuccess: " ✓",
	}[e.Level]

	r.mu.Lock()
	defer r.mu.Unlock()

	fmt.Fprintf(r.w, "%s [%s] %s\n", marker, e.Step, e.Message)
	for key, value := range e.Detail {
		fmt.Fprintf(r.w, "        %s: %s\n", key, value)
	}
}

// stepReporter binds a step name so individual steps do not repeat it.
type stepReporter struct {
	Reporter
	step string
}

func (r stepReporter) Emit(e Event) {
	if e.Step == "" {
		e.Step = r.step
	}
	r.Reporter.Emit(e)
}

// Infof emits an informational message.
func (r stepReporter) Infof(format string, args ...any) {
	r.Emit(Event{Level: LevelInfo, Message: fmt.Sprintf(format, args...)})
}

// Warnf emits a warning.
func (r stepReporter) Warnf(format string, args ...any) {
	r.Emit(Event{Level: LevelWarn, Message: fmt.Sprintf(format, args...)})
}

// Successf emits a success marker.
func (r stepReporter) Successf(format string, args ...any) {
	r.Emit(Event{Level: LevelSuccess, Message: fmt.Sprintf(format, args...)})
}

// Detail emits a message carrying structured findings.
func (r stepReporter) Detail(message string, detail map[string]string) {
	r.Emit(Event{Level: LevelInfo, Message: message, Detail: detail})
}
