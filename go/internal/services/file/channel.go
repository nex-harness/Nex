package file

// channel.go adapts the file Service to internal/rpc's Channel interface so
// the Registry can expose it under the "file" channel name (step 3.1
// registry, step 3.2 port). The proxy convention: one params object per
// call; results are plain values the rpc serializer maps.

import (
	"context"
	"fmt"

	"github.com/tylinux/nex/go/internal/rpc"
)

// ChannelName is ServiceChannels.File.
const ChannelName = "file"

// Channel adapts Service to rpc.Channel.
type Channel struct{ Service *Service }

var _ rpc.Channel = (*Channel)(nil)

// Call dispatches one IFileService method.
func (c *Channel) Call(ctx context.Context, method string, args []rpc.Value) (rpc.Value, error) {
	if len(args) == 0 || args[0] == nil {
		return nil, &rpc.Error{Message: "Method not found: " + method}
	}
	params, _ := args[0].(map[string]any)
	switch method {
	case "readdir":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		entries, err := c.Service.Readdir(ctx, path, optionalBool(params, "includeHidden"))
		if err != nil {
			return nil, err
		}
		return fileEntriesToValue(entries), nil
	case "stat":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		st, err := c.Service.Stat(ctx, path)
		if err != nil {
			return nil, err
		}
		return statToValue(st), nil
	case "checkFilesExist":
		paths, err := requireStrings(params, "paths", method)
		if err != nil {
			return nil, err
		}
		results, err := c.Service.CheckFilesExist(ctx, paths)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(results))
		for i, r := range results {
			out[i] = rpc.NewObject("path", r["path"], "exists", r["exists"])
		}
		return out, nil
	case "resolvePath":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		return c.Service.ResolvePath(ctx, path)
	case "ensureConversationWorkspace":
		path, created, err := c.Service.EnsureConversationWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", path, "created", created, "workspacePurpose", "conversation"), nil
	case "createDefaultWorkspace":
		path, err := c.Service.CreateDefaultWorkspace(ctx)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", path), nil
	case "createScratchWorkspace":
		name, err := requireString(params, "name", method)
		if err != nil {
			return nil, err
		}
		path, err := c.Service.CreateScratchWorkspace(ctx, name)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", path), nil
	case "readTextFile":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		slice, err := c.Service.ReadTextFile(ctx, path, optionalInt(params, "offset"), optionalInt(params, "length"))
		if err != nil {
			return nil, err
		}
		return rpc.NewObject(
			"path", slice.Path, "content", slice.Content, "offset", slice.Offset,
			"bytesRead", slice.BytesRead, "totalBytes", slice.TotalBytes,
			"truncated", slice.Truncated, "isBinary", slice.IsBinary,
		), nil
	case "readFileRange":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		offset, err := requireInt(params, "offset", method)
		if err != nil {
			return nil, err
		}
		length, err := requireInt(params, "length", method)
		if err != nil {
			return nil, err
		}
		data, err := c.Service.ReadFileRange(ctx, path, offset, length)
		if err != nil {
			return nil, err
		}
		return data, nil
	case "readMediaPreview":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		preview, err := c.Service.ReadMediaPreview(ctx, path, optionalInt(params, "maxBytes"))
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", preview.Path, "mediaType", preview.MediaType,
			"dataBase64", preview.DataBase64, "totalBytes", preview.TotalBytes), nil
	case "readBinaryPreview":
		path, err := requireString(params, "path", method)
		if err != nil {
			return nil, err
		}
		preview, err := c.Service.ReadBinaryPreview(ctx, path, optionalInt(params, "maxBytes"))
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("path", preview.Path, "dataBase64", preview.DataBase64,
			"totalBytes", preview.TotalBytes), nil
	case "searchWorkspaceFiles":
		return c.callSearch(ctx, params, method)
	case "listWorkspaceFilesLength":
		rootPath, err := requireString(params, "rootPath", method)
		if err != nil {
			return nil, err
		}
		n, err := c.Service.ListWorkspaceFilesLength(ctx, rootPath)
		if err != nil {
			return nil, err
		}
		return n, nil
	case "listWorkspaceFilesRange":
		rootPath, err := requireString(params, "rootPath", method)
		if err != nil {
			return nil, err
		}
		offset, err := requireInt(params, "offset", method)
		if err != nil {
			return nil, err
		}
		length, err := requireInt(params, "length", method)
		if err != nil {
			return nil, err
		}
		return c.Service.ListWorkspaceFilesRange(ctx, rootPath, offset, length)
	case "readWorkspaceFileSearchIgnore":
		rootPath, err := requireString(params, "rootPath", method)
		if err != nil {
			return nil, err
		}
		content, source, err := c.Service.ReadWorkspaceFileSearchIgnore(ctx, rootPath)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("content", content, "source", source), nil
	case "applyWorkspaceFileSearchIgnoreTransform":
		rootPath, err := requireString(params, "rootPath", method)
		if err != nil {
			return nil, err
		}
		transform, err := requireString(params, "transform", method)
		if err != nil {
			return nil, err
		}
		content, err := c.Service.ApplyWorkspaceFileSearchIgnoreTransform(ctx, rootPath, transform)
		if err != nil {
			return nil, err
		}
		return rpc.NewObject("content", content), nil
	case "writeWorkspaceFileSearchIgnore":
		rootPath, err := requireString(params, "rootPath", method)
		if err != nil {
			return nil, err
		}
		content, err := requireString(params, "content", method)
		if err != nil {
			return nil, err
		}
		return nil, c.Service.WriteWorkspaceFileSearchIgnore(ctx, rootPath, content)
	}
	return nil, &rpc.Error{Message: "Method not found: " + method}
}

// Listen: the file service has no events.
func (c *Channel) Listen(ctx context.Context, event string, _ rpc.Value) (<-chan rpc.Value, func(), error) {
	return nil, nil, &rpc.Error{Message: "Event not found: " + event}
}

func (c *Channel) callSearch(ctx context.Context, params map[string]any, method string) (rpc.Value, error) {
	rootPath, err := requireString(params, "rootPath", method)
	if err != nil {
		return nil, err
	}
	query, err := requireString(params, "query", method)
	if err != nil {
		return nil, err
	}
	identity := ""
	if v, ok := params["workspaceIdentity"].(string); ok {
		identity = v
	}
	refresh := false
	if v, ok := params["refresh"].(bool); ok {
		refresh = v
	}
	entries, err := c.Service.SearchWorkspaceFiles(ctx, rootPath, identity, query, optionalInt(params, "limit"), refresh)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = entryToValue(e)
	}
	return out, nil
}

func entryToValue(e workspaceEntry) rpc.Object {
	return rpc.NewObject("name", e.Name, "path", e.Path, "relativePath", e.RelativePath, "type", e.Type)
}

func fileEntriesToValue(entries []FileEntry) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = rpc.NewObject("name", e.Name, "path", e.Path, "type", e.Type, "isSymbolicLink", e.IsSymbolicLink)
	}
	return out
}

func statToValue(st FileStat) rpc.Object {
	obj := rpc.NewObject("path", st.Path, "type", st.Type)
	if st.Size != nil {
		obj.Set("size", *st.Size)
	}
	if st.MtimeMs != nil {
		obj.Set("mtimeMs", *st.MtimeMs)
	}
	return obj
}

// ---------------------------------------------------------------------------
// params decoding helpers
// ---------------------------------------------------------------------------

func requireString(params map[string]any, key, method string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", &rpc.Error{Message: fmt.Sprintf("Missing argument %s in %s", key, method)}
	}
	s, ok := v.(string)
	if !ok {
		return "", &rpc.Error{Message: fmt.Sprintf("Argument %s in %s must be a string", key, method)}
	}
	return s, nil
}

func requireStrings(params map[string]any, key, method string) ([]string, error) {
	v, ok := params[key]
	if !ok {
		return nil, &rpc.Error{Message: fmt.Sprintf("Missing argument %s in %s", key, method)}
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, &rpc.Error{Message: fmt.Sprintf("Argument %s in %s must be an array", key, method)}
	}
	out := make([]string, len(arr))
	for i, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, &rpc.Error{Message: fmt.Sprintf("Argument %s in %s must be an array of strings", key, method)}
		}
		out[i] = s
	}
	return out, nil
}

func requireInt(params map[string]any, key, method string) (int64, error) {
	v, ok := params[key]
	if !ok {
		return 0, &rpc.Error{Message: fmt.Sprintf("Missing argument %s in %s", key, method)}
	}
	f, ok := toFloat(v)
	if !ok {
		return 0, &rpc.Error{Message: fmt.Sprintf("Argument %s in %s must be a number", key, method)}
	}
	return int64(f), nil
}

func optionalInt(params map[string]any, key string) *int64 {
	v, ok := params[key]
	if !ok || v == nil {
		return nil
	}
	f, ok := toFloat(v)
	if !ok {
		return nil
	}
	i := int64(f)
	return &i
}

func optionalBool(params map[string]any, key string) bool {
	v, ok := params[key].(bool)
	return ok && v
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}
