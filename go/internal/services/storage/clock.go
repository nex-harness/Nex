// clock.go: the default wall clock (deps.now ?? Date.now).
package storage

import "time"

func timeNowMillis() int64 { return time.Now().UnixMilli() }
