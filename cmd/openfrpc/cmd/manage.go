package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zoefix/openfrp/internal/manage"
	"github.com/zoefix/openfrp/internal/storage"
)

// The management subcommands are the API the LuCI pages talk to.
//
// Every one writes a single JSON document to stdout: {"ok":true,"data":...} or
// {"ok":false,"error":"..."}. Always exiting 0 with a shaped body, rather than
// relying on exit status, is what lets the ucode backend report a real message
// to the browser instead of "the command failed".
//
// A file rather than a socket: management has to work while the tunnel daemon
// is stopped, which is exactly when someone is setting it up, and a listening
// port backed by cloud credentials is a surface this does not need.

// dbPath is where the management database lives. Overridable so a test or an
// operator can point at another file.
var dbPath = storage.DefaultPath

// reply is the envelope every management subcommand emits.
type reply struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// errReported marks a failure that has already been described on stdout, so
// main exits non-zero without printing it a second time.
var errReported = errors.New("reported")

// emit writes the envelope and reports whether it was a success.
func emit(data any, err error) error {
	response := reply{OK: err == nil, Data: data}
	if err != nil {
		response.Error = err.Error()
		response.Data = nil
	}

	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		// The result could not be encoded. Say so in a shape the caller can
		// still parse, rather than emitting nothing.
		fmt.Printf(`{"ok":false,"error":%q}`+"\n", marshalErr.Error())
		return marshalErr
	}

	fmt.Println(string(encoded))

	if err != nil {
		// The exit status has to say so too. The rpcd backend reads stdout and
		// ignores the status, but the job worker branches on it — and reported
		// a failed certificate issuance as "certificate issued" for as long as
		// this returned nil. A machine-readable error on stdout is not a
		// substitute for the one signal the caller actually checked.
		return errReported
	}
	return nil
}

// withService opens the database, runs fn, and closes it.
func withService(fn func(*manage.Service) (any, error)) error {
	service, err := manage.New(dbPath)
	if err != nil {
		if errors.Is(err, storage.ErrUnsupported) {
			return emit(nil, err)
		}
		return emit(nil, fmt.Errorf("open the management database: %w", err))
	}
	defer service.Close()

	return emit(fn(service))
}

// readStdinJSON decodes a JSON document from stdin into target.
//
// Input arrives this way rather than as flags because it carries provider
// credentials, and /proc/*/cmdline is readable by every local process on the
// router.
func readStdinJSON(target any) error {
	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read the request from stdin: %w", err)
	}
	if len(payload) == 0 {
		return errors.New("no request was supplied on stdin")
	}

	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("parse the request: %w", err)
	}
	return nil
}
