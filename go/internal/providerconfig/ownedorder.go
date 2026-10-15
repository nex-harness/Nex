// ownedorder.go: resolveOwnedOrder (owned-order.ts) — 规范化 Built-in/Personal
// 共同拥有的可排序成员 — plus the small set/dedupe helpers.
package providerconfig

// OwnedOrder ports resolveOwnedOrder (owned-order.ts): unsorted builtin
// before user order, unsorted personal after — 否则远端新增 Built-in 成员后，
// 下一次 Personal 写入会把它错误地挪到整个列表末尾.
func OwnedOrder(builtinIDs, personalIDs, requestedOrder []string) []string {
	builtin := uniqueInOrder(builtinIDs)
	builtinSet := stringSet(builtin)
	personal := make([]string, 0, len(personalIDs))
	for _, id := range personalIDs {
		if !builtinSet[id] {
			personal = append(personal, id)
		}
	}
	members := stringSet(append(append([]string{}, builtin...), personal...))
	var ordered []string
	for _, id := range requestedOrder {
		if members[id] && !contains(ordered, id) {
			ordered = append(ordered, id)
		}
	}
	orderedSet := stringSet(ordered)
	out := make([]string, 0, len(builtin)+len(ordered)+len(personal))
	for _, id := range builtin {
		if !orderedSet[id] {
			out = append(out, id)
		}
	}
	out = append(out, ordered...)
	for _, id := range personal {
		if !orderedSet[id] {
			out = append(out, id)
		}
	}
	return out
}

func uniqueInOrder(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
