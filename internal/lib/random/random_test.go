package random

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTGIDIsUniqueAndPositive(t *testing.T) {
	const n = 1000

	seen := make(map[int64]struct{}, n)
	for range n {
		id := TGID()
		require.Positive(t, id)

		_, dup := seen[id]
		require.False(t, dup, "TGID returned a duplicate: %d", id)

		seen[id] = struct{}{}
	}
}
