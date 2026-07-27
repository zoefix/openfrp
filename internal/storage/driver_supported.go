//go:build !mips && !mipsle && !mips64 && !mips64le

package storage

// Linking the driver is what registers the "sqlite" name with database/sql.
import _ "modernc.org/sqlite"

// driverAvailable reports whether a SQLite driver is compiled into this
// binary. See driver_unsupported.go for why it is not always true.
const driverAvailable = true
