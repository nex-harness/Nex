// channel_convert.go: rpc.Value builders for the discovery/import results
// (kept out of channel.go for the line limit).
package settingssync

import (
	"github.com/tylinux/nex/go/internal/rpc"
)

func discoveryToRPC(result *DiscoveryResult) rpc.Value {
	agents := make([]any, 0, len(result.Agents))
	for _, agent := range result.Agents {
		categories := make([]any, 0, len(agent.Categories))
		for _, category := range agent.Categories {
			categories = append(categories, categorySummaryToRPC(category))
		}
		agents = append(agents, rpc.NewObject(
			"agent", agent.Agent,
			"discovered", agent.Discovered,
			"categories", categories,
		))
	}
	return rpc.NewObject("agents", agents)
}

func categorySummaryToRPC(category CategorySummary) rpc.Value {
	obj := rpc.NewObject(
		"category", category.Category,
		"discoveredCount", category.DiscoveredCount,
		"importableCount", category.ImportableCount,
		"selectedByDefault", category.SelectedByDefault,
	)
	if category.SkippedCount != nil {
		obj.Set("skippedCount", *category.SkippedCount)
	}
	if category.SourcePaths != nil {
		obj.Set("sourcePaths", category.SourcePaths)
	}
	if category.SourceRoots != nil {
		roots := make([]any, 0, len(category.SourceRoots))
		for _, root := range category.SourceRoots {
			roots = append(roots, sourceRootToRPC(root))
		}
		obj.Set("sourceRoots", roots)
	}
	return obj
}

func sourceRootToRPC(root *SourceRootSummary) rpc.Value {
	obj := rpc.NewObject(
		"scope", root.Scope,
		"path", root.Path,
		"discoveredCount", root.DiscoveredCount,
		"importableCount", root.ImportableCount,
	)
	if root.SkippedCount != nil {
		obj.Set("skippedCount", *root.SkippedCount)
	}
	if root.Skills != nil {
		skills := make([]any, 0, len(root.Skills))
		for _, skill := range root.Skills {
			item := rpc.NewObject("name", skill.Name, "path", skill.Path, "importable", skill.Importable)
			if skill.SkipReason != "" {
				item.Set("skipReason", skill.SkipReason)
			}
			if skill.Version != "" {
				item.Set("version", skill.Version)
			}
			skills = append(skills, item)
		}
		obj.Set("skills", skills)
	}
	if root.Commands != nil {
		commands := make([]any, 0, len(root.Commands))
		for _, command := range root.Commands {
			item := rpc.NewObject("name", command.Name, "path", command.Path, "importable", command.Importable)
			if command.SkipReason != "" {
				item.Set("skipReason", command.SkipReason)
			}
			if command.Description != "" {
				item.Set("description", command.Description)
			}
			if command.ArgumentHint != "" {
				item.Set("argumentHint", command.ArgumentHint)
			}
			commands = append(commands, item)
		}
		obj.Set("commands", commands)
	}
	if root.Plugins != nil {
		plugins := make([]any, 0, len(root.Plugins))
		for _, plugin := range root.Plugins {
			item := rpc.NewObject("name", plugin.Name, "path", plugin.Path, "importable", plugin.Importable)
			if plugin.SkipReason != "" {
				item.Set("skipReason", plugin.SkipReason)
			}
			if plugin.Version != "" {
				item.Set("version", plugin.Version)
			}
			plugins = append(plugins, item)
		}
		obj.Set("plugins", plugins)
	}
	if root.McpServers != nil {
		servers := make([]any, 0, len(root.McpServers))
		for _, server := range root.McpServers {
			item := rpc.NewObject("name", server.Name, "path", server.Path, "importable", server.Importable)
			if server.SkipReason != "" {
				item.Set("skipReason", server.SkipReason)
			}
			servers = append(servers, item)
		}
		obj.Set("mcpServers", servers)
	}
	return obj
}

func importResultToRPC(result *ImportResult) rpc.Value {
	tasks := make([]any, 0, len(result.TaskResults))
	for _, task := range result.TaskResults {
		obj := rpc.NewObject(
			"agent", task.Agent,
			"category", task.Category,
			"status", task.Status,
			"importedCount", task.ImportedCount,
			"skippedCount", task.SkippedCount,
			"failedCount", task.FailedCount,
		)
		if task.SkillResults != nil {
			items := make([]any, 0, len(task.SkillResults))
			for _, item := range task.SkillResults {
				items = append(items, skillImportResultToRPC(item))
			}
			obj.Set("skillResults", items)
		}
		if task.CommandResults != nil {
			items := make([]any, 0, len(task.CommandResults))
			for _, item := range task.CommandResults {
				entry := rpc.NewObject("name", item.Name, "path", item.Path, "sourceScope", item.SourceScope, "status", item.Status)
				if item.SkipReason != "" {
					entry.Set("skipReason", item.SkipReason)
				}
				items = append(items, entry)
			}
			obj.Set("commandResults", items)
		}
		if task.PluginResults != nil {
			items := make([]any, 0, len(task.PluginResults))
			for _, item := range task.PluginResults {
				entry := rpc.NewObject("name", item.Name, "path", item.Path, "sourceScope", item.SourceScope, "status", item.Status)
				if item.SkipReason != "" {
					entry.Set("skipReason", item.SkipReason)
				}
				if item.Version != "" {
					entry.Set("version", item.Version)
				}
				items = append(items, entry)
			}
			obj.Set("pluginResults", items)
		}
		if task.McpResults != nil {
			items := make([]any, 0, len(task.McpResults))
			for _, item := range task.McpResults {
				entry := rpc.NewObject("name", item.Name, "path", item.Path, "sourceScope", item.SourceScope, "status", item.Status)
				if item.SkipReason != "" {
					entry.Set("skipReason", item.SkipReason)
				}
				items = append(items, entry)
			}
			obj.Set("mcpServerResults", items)
		}
		tasks = append(tasks, obj)
	}
	return rpc.NewObject(
		"successCount", result.SuccessCount,
		"skippedCount", result.SkippedCount,
		"failedCount", result.FailedCount,
		"taskResults", tasks,
	)
}

func skillImportResultToRPC(item SkillImportResult) rpc.Value {
	obj := rpc.NewObject("name", item.Name, "path", item.Path, "sourceScope", item.SourceScope, "status", item.Status)
	if item.SkipReason != "" {
		obj.Set("skipReason", item.SkipReason)
	}
	if item.Version != "" {
		obj.Set("version", item.Version)
	}
	return obj
}
