// sorthelper.go: tiny generic sort helpers.
package settingssync

import "sort"

func sortSlice[T any](items []T, less func(a, b T) bool) {
	sort.SliceStable(items, func(i, j int) bool { return less(items[i], items[j]) })
}

func intPtr(n int) *int { return &n }
