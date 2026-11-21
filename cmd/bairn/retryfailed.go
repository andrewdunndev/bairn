package main

import (
	"fmt"
	"sort"
	"strings"
)

// retryFailedIncompatible are the fetch flags --retry-failed refuses:
// it revisits entries an earlier run already accepted, so the filters
// that decide what is new have no meaning for it.
var retryFailedIncompatible = []string{"source", "include-system-posts", "allow-missing-state"}

// checkRetryFailed reports the flags set alongside --retry-failed that
// it cannot honour. set holds the names of the flags given explicitly.
func checkRetryFailed(set map[string]bool) error {
	var bad []string
	for _, name := range retryFailedIncompatible {
		if set[name] {
			bad = append(bad, "--"+name)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("--retry-failed cannot be combined with %s: it revisits only failed state entries, so the new-item filters do not apply", strings.Join(bad, ", "))
}
