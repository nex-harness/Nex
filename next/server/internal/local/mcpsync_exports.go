package local

// Exported constructor for the mcp-sync channel (app.go composition root).

// NewMcpSyncChannel serves mcp-sync from the config doc.
func NewMcpSyncChannel(manager *Manager) Channel {
	return newMcpSync(manager)
}
