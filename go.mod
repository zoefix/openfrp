module github.com/zoefix/openfrp

// Keep this at the lowest version our dependencies allow. Each OpenWrt branch
// pins its own Go toolchain in golang-values.mk (25.12 ships 1.26), so raising
// this needlessly is what breaks the older ipk build tracks.
go 1.25.0

require (
	github.com/go-acme/lego/v4 v4.35.2
	github.com/hashicorp/yamux v0.1.2
	github.com/pkg/sftp v1.13.11
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0
	modernc.org/sqlite v1.54.0
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/mattn/go-isatty v0.0.21 // indirect
	github.com/miekg/dns v1.1.72 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
