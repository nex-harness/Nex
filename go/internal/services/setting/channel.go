// channel.go adapts Service to internal/rpc's Channel under the "setting"
// channel name. Error messages copy the TS strings (UI branches on them).

package setting

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tylinux/nex/go/internal/rpc"
)

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one ISettingService method. The proxy convention passes
// the positional argument array; a single object argument is also accepted
// for the named-field spellings.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	switch method {
	case "get":
		settings, err := c.Service.Get(ctx)
		if err != nil {
			return nil, err
		}
		return orderedToRPC(settings), nil
	case "update":
		patch, expected, err := decodeUpdateArgs(args)
		if err != nil {
			return nil, err
		}
		if err := c.Service.Update(ctx, patch, expected); err != nil {
			return nil, err
		}
		return nil, nil
	case "updateDataBaseDir":
		var newDir *string
		if len(args) > 0 {
			if v, ok := args[0].(string); ok {
				newDir = &v
			} else if params, ok := args[0].(map[string]any); ok {
				if v, ok := params["newDir"].(string); ok {
					newDir = &v
				}
			}
		}
		if err := c.Service.UpdateDataBaseDir(ctx, newDir); err != nil {
			return nil, err
		}
		return nil, nil
	case "ensureDefaultProject":
		var homeDir string
		if len(args) > 0 {
			if v, ok := args[0].(string); ok {
				homeDir = v
			} else if params, ok := args[0].(map[string]any); ok {
				homeDir, _ = params["homedir"].(string)
			}
		}
		result, err := c.Service.EnsureDefaultProject(ctx, homeDir)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", result.Path, "created", result.Created), nil
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// decodeUpdateArgs accepts update(patch) and update(patch, expected).
func decodeUpdateArgs(args []rpc.Value) (map[string]any, *ExpectedAccountSettings, error) {
	patch := map[string]any{}
	var expected *ExpectedAccountSettings
	if len(args) == 0 {
		return patch, expected, nil
	}
	// Positional shape is the RPC contract: args[0] IS the patch. The
	// {patch: …} object-params shape is accepted only when a "patch" key is
	// present (it never collides: "patch" is not a settings field).
	if params, ok := args[0].(map[string]any); ok {
		if p, isParams := params["patch"].(map[string]any); isParams {
			patch = p
			if raw, ok := params["expectedAccountSettings"].(map[string]any); ok {
				expected = decodeExpectedAccountSettings(raw)
			}
			return patch, expected, nil
		}
		patch = params
	}
	if len(args) > 1 {
		if raw, ok := args[1].(map[string]any); ok {
			expected = decodeExpectedAccountSettings(raw)
		}
	}
	return patch, expected, nil
}

// Listen: the RPC contract has no events (onDidUpdate is host-private).
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

func decodeExpectedAccountSettings(raw map[string]any) *ExpectedAccountSettings {
	expected := &ExpectedAccountSettings{
		ProviderFamilyConnectionSelections: map[string]any{},
	}
	if domain, ok := raw["providerFamilyDomain"]; ok && domain != nil {
		if s, ok := domain.(string); ok {
			expected.ProviderFamilyDomain = s
			expected.ProviderFamilyDomainSet = true
		}
	}
	if selections, ok := raw["providerFamilyConnectionSelections"].(map[string]any); ok {
		expected.ProviderFamilyConnectionSelections = selections
	}
	return expected
}

// orderedToRPC converts an Ordered to the ordered rpc.Object so serialization
// preserves field order.
func orderedToRPC(o *Ordered) rpc.Object {
	obj := rpc.NewObject()
	for _, key := range o.Keys() {
		value, _ := o.Get(key)
		obj.Set(key, toRPCValue(value))
	}
	return obj
}

func toRPCValue(v any) rpc.Value {
	switch t := v.(type) {
	case *Ordered:
		return orderedToRPC(t)
	case map[string]any:
		obj := rpc.NewObject()
		for key, item := range t {
			obj.Set(key, toRPCValue(item))
		}
		return obj
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = toRPCValue(item)
		}
		return out
	default:
		return v
	}
}

// itoa/ftoa are tiny formatters so normalize.go stays free of strconv imports
// drifting between files.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// urlUnescape ports decodeURIComponent (percent + nothing else; Go's
// url.QueryUnescape also handles '+' which decodeURIComponent does NOT, so
// the '+' is pre-escaped).
func urlUnescape(s string) (string, error) {
	unescaped, err := unescapePercent(s)
	if err != nil {
		return "", err
	}
	return unescaped, nil
}

func unescapePercent(s string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' {
			if i+2 >= len(s)+1 || i+2 > len(s)-1+1 {
				return "", errBadEscape
			}
			if i+2 >= len(s) {
				return "", errBadEscape
			}
			hi, ok1 := hexVal(s[i+1])
			lo, ok2 := hexVal(s[i+2])
			if !ok1 || !ok2 {
				return "", errBadEscape
			}
			out.WriteByte(byte(hi<<4 | lo))
			i += 2
			continue
		}
		out.WriteByte(s[i])
	}
	return out.String(), nil
}

type escapeError string

func (e escapeError) Error() string { return "invalid URI escape: " + string(e) }

var errBadEscape = escapeError("%")

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	default:
		return 0, false
	}
}

// jsonUnmarshal is a test indirection over encoding/json.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
