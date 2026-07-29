//go:build !mips && !mipsle && !mips64 && !mips64le

package storage

import _ "modernc.org/sqlite"

const driverAvailable = true
