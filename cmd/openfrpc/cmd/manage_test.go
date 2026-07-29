package cmd

import (
	"errors"
	"testing"
)

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
