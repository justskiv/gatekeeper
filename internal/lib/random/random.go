// Package random provides process-unique identifiers for test data that
// gofakeit cannot safely supply. It depends only on the standard
// library, so store's own tests can use it without the import cycle
// that testutil would introduce.
package random

import "sync/atomic"

// tgIDCounter makes Telegram IDs monotonic and thus unique within a
// process, so generated users never collide on the unique indexes and
// foreign keys keyed off tg_id.
var tgIDCounter atomic.Int64

// tgIDBase keeps generated IDs well above the small literals (1, 7, 42…)
// that a few tests still spell out explicitly, so the two never clash.
const tgIDBase int64 = 1_000_000_000

// TGID returns a process-unique positive Telegram user ID.
func TGID() int64 {
	return tgIDBase + tgIDCounter.Add(1)
}
