// Package providers links every DNS provider implementation.
//
// Providers register themselves from init, so importing this package is what
// populates the registry. Anything that needs to resolve a provider by key
// imports this rather than each implementation, which keeps the list of
// supported services in exactly one place.
package providers

import (
	_ "github.com/zoefix/openfrp/internal/dns/providers/aliyun"
	_ "github.com/zoefix/openfrp/internal/dns/providers/cloudflare"
	_ "github.com/zoefix/openfrp/internal/dns/providers/dnspod"
	_ "github.com/zoefix/openfrp/internal/dns/providers/huawei"
	_ "github.com/zoefix/openfrp/internal/dns/providers/west"
)
