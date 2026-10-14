package file

// paths.go supplies the paths the file service needs from the 3.1 paths
// package: getConversationWorkspaceDir(). The base-dir resolution (explicit
// setter > NEX_DATA_BASE_DIR > HOME) is owned by internal/services/paths; the
// file service consumes it through this indirection so tests can pin it.

import (
	"os"

	"github.com/tylinux/nex/go/internal/services/paths"
)

// conversationWorkspaceDirProvider is swappable for tests.
var conversationWorkspaceDirProvider = defaultConversationWorkspaceDir

func defaultConversationWorkspaceDir() string {
	p := paths.New("", homeDir())
	return p.ConversationWorkspaceDir()
}

func conversationWorkspaceDir() string {
	return conversationWorkspaceDirProvider()
}

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}
