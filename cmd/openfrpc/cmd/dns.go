package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zoefix/openfrp/internal/dns"
	"github.com/zoefix/openfrp/internal/manage"
)

func init() {
	register(&Command{
		Name:    "dns",
		Summary: "manage DNS provider accounts and records",
		Run:     runDNS,
	})
}

func dnsUsage() {
	fmt.Fprint(os.Stderr, `Usage: openfrpc dns <action> [flags]

Actions:
  providers                    list supported providers and their credential forms
  accounts                     list stored accounts, credentials redacted
  account-add                  create an account; JSON on stdin
  account-update               edit an account; JSON on stdin
  account-delete   -id N       remove an account
  account-test     -id N       verify stored credentials against the provider
  capabilities     -id N       report what the account's provider supports
  domains          -id N       list zones the account manages
  records          -id N -zone Z          list records in a zone
  record-add       -id N -zone Z          create a record; JSON on stdin
  record-update    -id N -zone Z          edit a record; JSON on stdin
  record-delete    -id N -zone Z -record R  remove a record
  record-status    -id N -zone Z -record R -enabled BOOL

Every action writes one JSON document to stdout. Requests carrying credentials
arrive on stdin, never as flags, because /proc/*/cmdline is world readable.
`)
}

func runDNS(ctx context.Context, args []string) error {
	if len(args) == 0 {
		dnsUsage()
		os.Exit(2)
	}

	action := args[0]
	fs := flag.NewFlagSet("dns "+action, flag.ExitOnError)
	var (
		id       = fs.Int64("id", 0, "account id")
		zone     = fs.String("zone", "", "DNS zone")
		recordID = fs.String("record", "", "provider record id")
		enabled  = fs.Bool("enabled", true, "record enabled state")
		keyword  = fs.String("keyword", "", "filter by substring")
		page     = fs.Int("page", 1, "page number")
		pageSize = fs.Int("page-size", 100, "results per page")
		database = fs.String("db", dbPath, "management database")
	)
	fs.Usage = dnsUsage
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	dbPath = *database

	opts := dns.ListOptions{Keyword: *keyword, Page: *page, PageSize: *pageSize}

	return withService(func(service *manage.Service) (any, error) {
		switch action {
		case "providers":
			return service.Providers(), nil

		case "accounts":
			return service.ListAccounts(ctx)

		case "account-add":
			var in manage.AccountInput
			if err := readStdinJSON(&in); err != nil {
				return nil, err
			}
			return service.CreateAccount(ctx, in)

		case "account-update":
			var in manage.AccountInput
			if err := readStdinJSON(&in); err != nil {
				return nil, err
			}
			return service.UpdateAccount(ctx, in)

		case "account-delete":
			return nil, service.DeleteAccount(ctx, *id)

		case "account-test":
			if err := service.TestAccount(ctx, *id); err != nil {
				return nil, err
			}
			return map[string]string{"result": "the credentials work"}, nil

		case "capabilities":
			return service.Capabilities(ctx, *id)

		case "domains":
			return service.ListDomains(ctx, *id, opts)

		case "records":
			if *zone == "" {
				return nil, fmt.Errorf("dns %s: -zone is required", action)
			}
			return service.ListRecords(ctx, *id, *zone, opts)

		case "record-add":
			record, err := recordFromStdin()
			if err != nil {
				return nil, err
			}
			newID, err := service.AddRecord(ctx, *id, *zone, record)
			if err != nil {
				return nil, err
			}
			return map[string]string{"id": newID}, nil

		case "record-update":
			record, err := recordFromStdin()
			if err != nil {
				return nil, err
			}
			return nil, service.UpdateRecord(ctx, *id, *zone, record)

		case "record-delete":
			return nil, service.DeleteRecord(ctx, *id, *zone, *recordID)

		case "record-status":
			return nil, service.SetRecordStatus(ctx, *id, *zone, *recordID, *enabled)

		default:
			return nil, fmt.Errorf("dns: unknown action %q", action)
		}
	})
}

// recordFromStdin reads a record definition.
func recordFromStdin() (dns.Record, error) {
	var record dns.Record
	if err := readStdinJSON(&record); err != nil {
		return dns.Record{}, err
	}
	return record, nil
}
