// env.go: process env + home resolution helpers.
package settingssync

import (
	"os"
	"strings"
)

func defaultEnvReader() map[string]string {
	out := map[string]string{}
	for _, entry := range os.Environ() {
		for i := 0; i < len(entry); i++ {
			if entry[i] == '=' {
				out[entry[:i]] = entry[i+1:]
				break
			}
		}
	}
	return out
}

func trimSpaceOf(s string) string { return strings.TrimSpace(s) }

func userHomeDirOrEmpty() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
