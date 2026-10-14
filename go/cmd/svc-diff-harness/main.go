// svc-diff-harness drives the step-3.2 Go services through their rpc.Channel
// adapters with the same call list svc-diff.mjs uses on the Node side. It
// reads {"fixtureRoot", "cases"} from stdin and writes the results array as
// JSON on stdout, in the same normalized shape the mjs side produces.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tylinux/nex/go/internal/rpc"
	"github.com/tylinux/nex/go/internal/services/credential"
	"github.com/tylinux/nex/go/internal/services/file"
	"github.com/tylinux/nex/go/internal/services/filewatcher"
	"github.com/tylinux/nex/go/internal/services/mediapreview"
	"github.com/tylinux/nex/go/internal/services/onboarding"
	"github.com/tylinux/nex/go/internal/services/setting"
	"github.com/tylinux/nex/go/internal/services/settingssync"
	"github.com/tylinux/nex/go/internal/services/system"
)

// settingBridge adapts *setting.Service to settingssync.SettingService
// (map-based settings access for the first-run prompt state).
type settingBridge struct{ svc *setting.Service }

func (b settingBridge) Get(ctx context.Context) (map[string]any, error) {
	settings, err := b.svc.Get(ctx)
	if err != nil {
		return nil, err
	}
	return settings.Map(), nil
}

func (b settingBridge) Update(ctx context.Context, patch map[string]any) error {
	return b.svc.Update(ctx, patch, nil)
}

type harnessCase struct {
	Method string      `json:"method"`
	Args   []rpc.Value `json:"args"`
}

type input struct {
	FixtureRoot string        `json:"fixtureRoot"`
	Cases       []harnessCase `json:"cases"`
}

func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fmt.Fprintf(os.Stderr, "svc-diff-harness: decode input: %v\n", err)
		os.Exit(2)
	}
	svcName := os.Args[1]

	var channel rpc.Channel
	switch svcName {
	case "system":
		channel = &system.Channel{Service: system.NewService(system.Options{})}
	case "file":
		channel = &file.Channel{Service: file.NewService(nil)}
	case "filewatcher":
		channel = &filewatcher.Channel{Service: filewatcher.NewService()}
	case "media":
		channel = &mediapreview.Channel{Service: mediapreview.NewService(mediapreview.Options{
			FileService: file.NewService(nil),
		})}
	// Step 3.5 (settings family): the mjs side sets NEX_DESKTOP_HOME_DIR to
	// this engine's fixture root before spawning the harness; the services
	// resolve their state files from it (resolveUserHomeDir parity).
	case "setting":
		channel = &setting.Channel{Service: setting.NewService(setting.ServiceOptions{})}
	case "onboarding":
		channel = &onboarding.Channel{Service: onboarding.NewService(onboarding.Options{})}
	case "credential":
		channel = &credential.Channel{Service: credential.NewService(credential.Options{})}
	case "settings-sync":
		settingService := setting.NewService(setting.ServiceOptions{})
		channel = &settingssync.Channel{Service: settingssync.NewService(settingssync.Options{
			SettingService: settingBridge{svc: settingService},
		})}
	default:
		fmt.Fprintf(os.Stderr, "svc-diff-harness: unknown service %q\n", svcName)
		os.Exit(2)
	}

	results := make([]any, 0, len(in.Cases))
	for _, c := range in.Cases {
		args := c.Args
		if args == nil {
			args = []rpc.Value{nil}
		}
		value, err := channel.Call(context.Background(), c.Method, args)
		if err != nil {
			results = append(results, map[string]any{
				"ok": false,
				"error": map[string]any{
					"name":    "Error",
					"message": normalizeString(err.Error(), in.FixtureRoot),
				},
			})
			continue
		}
		if value == nil {
			// void methods serialize as {"ok":true} — no result key
			results = append(results, map[string]any{"ok": true})
		} else if _, isNull := value.(rpc.NullType); isNull {
			// The TS `null` result: the key IS present with a null value.
			results = append(results, map[string]any{"ok": true, "result": nil})
		} else {
			results = append(results, map[string]any{"ok": true, "result": normalize(value, in.FixtureRoot)})
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(results); err != nil {
		fmt.Fprintf(os.Stderr, "svc-diff-harness: encode: %v\n", err)
		os.Exit(2)
	}
}

// normalize mirrors svc-diff.mjs's normalize: fixture/ROOT path scrubbing and
// timing-field redaction, so both sides compare equal.
func normalize(v rpc.Value, fixtureRoot string) any {
	switch t := v.(type) {
	case string:
		return normalizeString(t, fixtureRoot)
	case []byte:
		return map[string]any{"__bytes": bytesToInts(t)}
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = normalize(item, fixtureRoot)
		}
		return out
	case []string:
		out := make([]string, len(t))
		for i, item := range t {
			out[i] = normalizeString(item, fixtureRoot)
		}
		return out
	case rpc.Object:
		out := map[string]any{}
		for i, k := range t.Keys {
			out[k] = redact(k, normalize(t.Values[i], fixtureRoot))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, item := range t {
			out[k] = redact(k, normalize(item, fixtureRoot))
		}
		return out
	default:
		return v
	}
}

func redact(key string, v any) any {
	switch key {
	case "mtimeMs", "checkedAt", "latencyMs":
		return "<" + key + ">"
	case "error":
		if _, ok := v.(string); ok {
			return "<error>"
		}
	}
	return v
}

func bytesToInts(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

func normalizeString(s, fixtureRoot string) string {
	s = strings.ReplaceAll(s, fixtureRoot, "<FIXTURE>")
	s = strings.ReplaceAll(s, repoRoot(), "<ROOT>")
	return s
}

func repoRoot() string {
	cwd, _ := os.Getwd()
	// running via `go run ./cmd/svc-diff-harness` from go/: the module dir is cwd
	return cwd + "/.."
}
