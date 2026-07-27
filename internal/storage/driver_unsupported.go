//go:build mips || mipsle || mips64 || mips64le

package storage

// modernc.org/sqlite has no MIPS port: modernc.org/libc excludes those
// architectures with a build constraint, so importing the driver does not fail
// to link, it fails to compile.
//
// The alternative would be CGO and mattn/go-sqlite3, which would need a C
// cross-toolchain per target and end the single static binary. Between losing
// DNS and certificate management on MIPS and losing the distribution model
// everywhere, this is the cheaper loss — the project targets x86 routers, and
// MIPS devices were explicitly out of scope from the start.
//
// The tunnel does not touch storage, so on MIPS everything the plugin exists
// for still works; only DNS and certificate management are unavailable, and
// they say so instead of failing obscurely.
const driverAvailable = false
