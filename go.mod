module github.com/zoefix/openfrp

// Keep this at the lowest version our dependencies allow. Each OpenWrt branch
// pins its own Go toolchain in golang-values.mk (25.12 ships 1.26), so raising
// this needlessly is what breaks the older ipk build tracks.
go 1.25.0

require (
	github.com/hashicorp/yamux v0.1.2
	golang.org/x/net v0.57.0
)

require (
	github.com/kr/fs v0.1.0 // indirect
	github.com/pkg/sftp v1.13.11 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
