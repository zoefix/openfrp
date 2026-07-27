package cmd

import (
	"errors"
	"testing"
)

// TestFailedActionsReportThroughTheExitStatus is the guard against the worst
// kind of failure this tool can have: one that reports success.
//
// The management subcommands describe themselves as a JSON envelope on stdout,
// which the rpcd backend parses and the browser renders. The job worker does
// not parse anything — it branches on the exit status. While emit returned nil
// on failure, a certificate issuance that could not run at all was logged as
// "certificate issued" and the dialog said so too.
func TestFailedActionsReportThroughTheExitStatus(t *testing.T) {
	if err := emit(map[string]string{"result": "fine"}, nil); err != nil {
		t.Errorf("a successful action returned %v, want nil", err)
	}

	err := emit(nil, errors.New("order 4 has no DNS account"))
	if !errors.Is(err, errReported) {
		t.Errorf("a failed action returned %v, want errReported so the caller "+
			"exits non-zero", err)
	}
}
