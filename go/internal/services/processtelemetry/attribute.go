// attribute.go ports attributeHostProcessTree: every Host descendant is
// attributed to the NEAREST known ancestor — an MCP root pid to its plugin, an
// agent pid to the base `cli` group, anything else to base `host`.
//
// The Host itself is never in the result (its metrics come from
// app.getAppMetrics), and processes registered in the agent registry but not
// in the Host subtree (ppid reordered to 1 by an init) are still appended so
// the topology does not lose a row.
package processtelemetry

import "sort"

// Owner is the attribution target of a process subtree.
type Owner struct {
	// Kind is one of ownerAgent / ownerMCP / ownerBuiltin.
	Kind       string
	Agent      Agent
	Child      ChildProcess
	PluginName string
}

// Owner kinds.
const (
	ownerAgent   = "agent"
	ownerMCP     = "mcp"
	ownerBuiltin = "builtin"
)

// AttributeOptions ports AttributeHostProcessTreeOptions.
type AttributeOptions struct {
	Samples map[int]Sample
	HostPID int
	Agents  []Agent
	// BuiltinPluginPids maps a pid the Host manages directly (e.g. the Windows
	// CUA helper) to its plugin name.
	BuiltinPluginPids map[int]string
}

// AttributeHostProcessTree ports attributeHostProcessTree.
func AttributeHostProcessTree(options AttributeOptions) []UsageProcess {
	childrenByParent := map[int][]int{}
	for pid, sample := range options.Samples {
		childrenByParent[sample.PPID] = append(childrenByParent[sample.PPID], pid)
	}
	ownerRoots := map[int]Owner{}
	for _, agent := range options.Agents {
		ownerRoots[agent.PID] = Owner{Kind: ownerAgent, Agent: agent}
		for _, child := range agent.Children {
			ownerRoots[child.PID] = Owner{Kind: ownerMCP, Child: child}
		}
	}
	for pid, pluginName := range options.BuiltinPluginPids {
		ownerRoots[pid] = Owner{Kind: ownerBuiltin, PluginName: pluginName}
	}

	rows := []UsageProcess{}
	visited := map[int]bool{options.HostPID: true}
	// The walk is depth-first over a fixed snapshot map, so recursion is
	// bounded by the process depth; the visited set breaks any cycle a
	// recycled PID could form.
	var visit func(pid int, inherited Owner, hasInherited bool)
	visit = func(pid int, inherited Owner, hasInherited bool) {
		for _, childPID := range childrenByParent[pid] {
			if visited[childPID] {
				continue
			}
			visited[childPID] = true
			sample, exists := options.Samples[childPID]
			if !exists {
				continue
			}
			rootOwner, isRoot := ownerRoots[childPID]
			owner, hasOwner := rootOwner, isRoot
			if !isRoot {
				owner, hasOwner = inherited, hasInherited
			}
			rows = append(rows, ownerToRow(owner, hasOwner, sample, isRoot))
			visit(childPID, owner, hasOwner)
		}
	}
	visit(options.HostPID, Owner{}, false)

	// Registered processes that are not in the Host subtree still belong to a
	// group; dropping them would make a running plugin vanish from the panel.
	ownerRootPids := make([]int, 0, len(ownerRoots))
	for pid := range ownerRoots {
		ownerRootPids = append(ownerRootPids, pid)
	}
	sort.Ints(ownerRootPids)
	for _, pid := range ownerRootPids {
		if visited[pid] {
			continue
		}
		owner := ownerRoots[pid]
		sample, exists := options.Samples[pid]
		if !exists {
			continue
		}
		visited[pid] = true
		rows = append(rows, ownerToRow(owner, true, sample, true))
		visit(pid, owner, true)
	}

	sort.Slice(rows, func(left, right int) bool { return rows[left].PID < rows[right].PID })
	return rows
}

// ownerToRow ports ownerToRow.
func ownerToRow(owner Owner, hasOwner bool, sample Sample, isOwnerRoot bool) UsageProcess {
	row := UsageProcess{
		PID:         sample.PID,
		CPUPercent:  sample.CPUPercent,
		MemoryBytes: sample.RSSKb * 1024,
	}
	if !hasOwner {
		row.Name = CommandDisplayName(sample.Command)
		row.Category = CategoryBase
		row.GroupKey = "host"
		row.GroupLabel = "host"
		return row
	}
	switch owner.Kind {
	case ownerAgent:
		// Only the agent root itself carries the nex-* name; its own
		// descendants keep the plain command name.
		if isOwnerRoot {
			row.Name = FormatNexAgentProcessName(owner.Agent.Provider, owner.Agent.WorkspacePath)
		} else {
			row.Name = CommandDisplayName(sample.Command)
		}
		row.Category = CategoryBase
		row.GroupKey = "cli"
		row.GroupLabel = "cli"
	case ownerBuiltin:
		row.Name = CommandDisplayName(sample.Command)
		row.Category = CategoryBuiltinPlugin
		row.GroupKey = owner.PluginName
		row.GroupLabel = owner.PluginName
	default:
		child := owner.Child
		groupLabel := child.PluginName
		if groupLabel == "" {
			groupLabel = child.ServerName
		}
		if isOwnerRoot {
			row.Name = child.ServerName
		} else {
			row.Name = CommandDisplayName(sample.Command)
		}
		// User decision: official-marketplace plugins are builtin, everything
		// else (third-party marketplace + custom MCP) is community.
		row.Category = CategoryCommunityPlugin
		if child.MCPSource == MCPSourceBuiltin {
			row.Category = CategoryBuiltinPlugin
		}
		row.GroupKey = child.MCPSource + ":" + groupLabel
		row.GroupLabel = groupLabel
	}
	return row
}
