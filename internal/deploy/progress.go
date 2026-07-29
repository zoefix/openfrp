package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type Level string

const (
	LevelInfo    Level = "info"
	LevelWarn    Level = "warn"
	LevelError   Level = "error"
	LevelSuccess Level = "success"
)

type Event struct {
	Time    string `json:"time"`
	Step    string `json:"step"`
	Level   Level  `json:"level"`
	Message string `json:"message"`

	Detail map[string]string `json:"detail,omitempty"`
}

type Reporter interface {
	Emit(Event)
}

type JSONReporter struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

func NewJSONReporter(w io.Writer) *JSONReporter {
	return &JSONReporter{w: w, now: time.Now}
}

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

type TextReporter struct {
	mu sync.Mutex
	w  io.Writer
}

func NewTextReporter(w io.Writer) *TextReporter {
	return &TextReporter{w: w}
}

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

func (r stepReporter) Infof(format string, args ...any) {
	r.Emit(Event{Level: LevelInfo, Message: fmt.Sprintf(format, args...)})
}

func (r stepReporter) Warnf(format string, args ...any) {
	r.Emit(Event{Level: LevelWarn, Message: fmt.Sprintf(format, args...)})
}

func (r stepReporter) Successf(format string, args ...any) {
	r.Emit(Event{Level: LevelSuccess, Message: fmt.Sprintf(format, args...)})
}

func (r stepReporter) Detail(message string, detail map[string]string) {
	r.Emit(Event{Level: LevelInfo, Message: message, Detail: detail})
}
