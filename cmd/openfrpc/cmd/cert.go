package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zoefix/openfrp/internal/manage"
)

func init() {
	register(&Command{
		Name:    "cert",
		Summary: "manage TLS certificate orders",
		Run:     runCert,
	})
}

func certUsage() {
	fmt.Fprint(os.Stderr, `Usage: openfrpc cert <action> [flags]

Actions:
  cas                     list supported certificate authorities
  keytypes                list supported key algorithms
  orders                  list certificate orders
  order-add               create an order; JSON on stdin
  order-delete  -id N     remove an order and its history
  events        -id N     show an order's history
  export        -id N     print the issued chain as PEM
  issue         -id N     obtain or renew the certificate
  eab                     store external account binding credentials; JSON on stdin

issue talks to the CA and waits for DNS propagation, so it takes minutes and
must be run as a detached job rather than inside an rpcd call.
`)
}

func runCert(ctx context.Context, args []string) error {
	if len(args) == 0 {
		certUsage()
		os.Exit(2)
	}

	action := args[0]
	fs := flag.NewFlagSet("cert "+action, flag.ExitOnError)
	var (
		id       = fs.Int64("id", 0, "order id")
		limit    = fs.Int("limit", 50, "how many events to return")
		database = fs.String("db", dbPath, "management database")
	)
	fs.Usage = certUsage
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dbPath = *database

	// issue is the one action that reports as it goes. Its progress lines go to
	// stderr so stdout stays a single parseable JSON document; the job worker
	// captures both into one log, so the operator sees them interleaved.
	progress := func(message string) {
		fmt.Fprintln(os.Stderr, "==> "+message)
	}

	return withService(func(service *manage.Service) (any, error) {
		switch action {
		case "cas":
			return service.CAs(), nil

		case "keytypes":
			return service.KeyTypes(), nil

		case "orders":
			return service.ListOrders(ctx)

		case "order-add":
			var in manage.OrderInput
			if err := readStdinJSON(&in); err != nil {
				return nil, err
			}
			return service.CreateOrder(ctx, in)

		case "order-delete":
			return nil, service.DeleteOrder(ctx, *id)

		case "events":
			return service.OrderEvents(ctx, *id, *limit)

		case "export":
			material, err := service.Material(ctx, *id)
			if err != nil {
				return nil, err
			}
			return map[string]string{"fullchain_pem": string(material)}, nil

		case "issue":
			if err := service.Issue(ctx, *id, progress); err != nil {
				return nil, err
			}
			return map[string]string{"result": "the certificate was issued"}, nil

		case "eab":
			var in struct {
				CA    string `json:"ca"`
				Email string `json:"email"`
				KeyID string `json:"key_id"`
				HMAC  string `json:"hmac"`
			}
			if err := readStdinJSON(&in); err != nil {
				return nil, err
			}
			return nil, service.SetEAB(ctx, in.CA, in.Email, in.KeyID, in.HMAC)

		default:
			return nil, fmt.Errorf("cert: unknown action %q", action)
		}
	})
}
