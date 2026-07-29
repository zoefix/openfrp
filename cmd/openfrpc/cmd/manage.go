package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/zoefix/openfrp/internal/config"
	"github.com/zoefix/openfrp/internal/manage"
	"github.com/zoefix/openfrp/internal/storage"
	"github.com/zoefix/openfrp/internal/version"
)

var dbPath = storage.DefaultPath

type reply struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

var errReported = errors.New("reported")

func emit(data any, err error) error {
	response := reply{OK: err == nil, Data: data}
	if err != nil {
		response.Error = err.Error()
		response.Data = nil
	}

	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {

		fmt.Printf(`{"ok":false,"error":%q}`+"\n", marshalErr.Error())
		return marshalErr
	}

	fmt.Println(string(encoded))

	if err != nil {

		return errReported
	}
	return nil
}

func withService(fn func(*manage.Service) (any, error)) error {
	service, err := manage.New(dbPath)
	if err != nil {
		if errors.Is(err, storage.ErrUnsupported) {
			return emit(nil, err)
		}
		return emit(nil, fmt.Errorf("open the management database: %w", err))
	}
	defer service.Close()

	attachServers(service)

	return emit(fn(service))
}

func attachServers(service *manage.Service) {
	cfg, err := config.LoadClient(clientConfigPath)
	if err != nil {
		return
	}
	service.SetHTTPChallengeServers(cfg.Upstreams(), version.Short())
}

var clientConfigPath = "/var/etc/openfrp.json"

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
