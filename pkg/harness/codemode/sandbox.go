package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
)

const (
	defaultSandboxTimeoutMS  = 30_000
	maxSandboxTimeoutMS      = 120_000
	defaultMaxOutputBytes    = 16 * 1024
	maxSessionStoreQuotaByte = 256 * 1024
)

// SubCallAuthorizer enforces plugin OnToolCall hooks and PermissionGuard approvals on EVERY inner tools.* call!
type SubCallAuthorizer func(ctx context.Context, fullToolName string, readOnly bool, args json.RawMessage) error

// SessionKV provides bounded per-session store(k,v)/load(k) across multiple codemode calls without polluting LLM context.
type SessionKV struct {
	mu    sync.RWMutex
	items map[string]string
	bytes int
}

func NewSessionKV() *SessionKV {
	return &SessionKV{items: make(map[string]string)}
}

func (kv *SessionKV) Store(key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return err
	}
	kv.mu.Lock()
	defer kv.mu.Unlock()
	prev := len(kv.items[key])
	nextTotal := kv.bytes - prev + len(raw)
	if nextTotal > maxSessionStoreQuotaByte {
		return errors.New("codemode store quota exceeded (256KB per session)")
	}
	kv.items[key] = string(raw)
	kv.bytes = nextTotal
	return nil
}

func (kv *SessionKV) Load(key string) any {
	kv.mu.RLock()
	raw, ok := kv.items[key]
	kv.mu.RUnlock()
	if !ok {
		return nil
	}
	var out any
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ModelBridge provides optional access to classifier (e.g. Jev) and image generation models inside codemode scripts.
type ModelBridge struct {
	ResolveClassifier func(ctx context.Context, modelID string) (ai.ClassifierProvider, string, error)
	ResolveImage      func(ctx context.Context, modelID string) (ai.ImageProvider, string, error)
}

// BrowserCaller invokes a browser action (same JSON schema as the `browser` tool) from inside codemode scripts,
// enabling Code-First multi-step browser automation in a single LLM turn.
type BrowserCaller func(ctx context.Context, action string, args map[string]any) (string, error)

// Tool exposes the single unified `codemode` LLM tool backed by an isolated zero-privilege goja sandbox.
type Tool struct {
	pool          *Pool
	kv            *SessionKV
	authorizer    SubCallAuthorizer
	modelBridge   *ModelBridge
	browserCaller BrowserCaller
	workspacePath string
}

// NewTool constructs the `codemode` tool for a session.
func NewTool(pool *Pool, kv *SessionKV, authorizer SubCallAuthorizer) *Tool {
	if kv == nil {
		kv = NewSessionKV()
	}
	return &Tool{
		pool:       pool,
		kv:         kv,
		authorizer: authorizer,
	}
}

// SetModelBridge attaches classifier and image model resolvers to the codemode sandbox.
func (t *Tool) SetModelBridge(b *ModelBridge) {
	t.modelBridge = b
}

// SetBrowserCaller attaches the session browser executor so codemode scripts can run
// multi-step browser automation (`await browser.open(...)`, `await browser.snapshot()`,
// `await browser.click(...)`, `await browser.fill(...)`, `await browser.eval(...)`, etc.)
// in a single turn without LLM round-trips.
func (t *Tool) SetBrowserCaller(fn BrowserCaller) {
	t.browserCaller = fn
}

// SetWorkspacePath scopes `searchTools` discovery and workspace MCP visibility checks to a session workspace.
func (t *Tool) SetWorkspacePath(workspacePath string) {
	t.workspacePath = workspacePath
}

func (t *Tool) Name() string     { return "codemode" }
func (t *Tool) Category() string { return "mcp" }
func (t *Tool) IsReadOnly(_ json.RawMessage) bool {
	// Inner tools.* calls individually pass through SubCallAuthorizer!
	return true
}

func (t *Tool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{
		Name:        "codemode",
		Category:    "mcp",
		Description: "Run async JavaScript in an isolated, zero-privilege goja sandbox to discover and orchestrate MCP tools concurrently.\n\n- The sandbox has no filesystem, process, or network access of its own. Its only capabilities are `searchTools(query)`, `describeTool(name)`, `tools.<namespace>.<tool>(args)` (async), `callTool(\"<ns>.<tool>\", args)`, `text(value)`, and `store(k,v)` / `load(k)` for values that must survive across calls.\n- Discover first: call `searchTools` to find candidate tools, then `describeTool` for exact argument schemas before invoking them.\n- Run independent calls in parallel with `Promise.all([tools.ns.a(...), tools.ns.b(...)])`, then filter and reduce the noisy JSON inside the script and return only a concise result.\n- Every inner `tools.*` call still passes through plugin hooks and the permission system; a denial surfaces as a rejected promise, so handle it rather than retrying blindly.\n- Optional first line `// @options: {\"timeout_ms\": 30000, \"max_output_bytes\": 16384}` tunes limits for heavier scripts.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"code": {
					"type": "string",
					"description": "Async JavaScript function body (supports top-level await, return, Promise.all, searchTools, describeTool, tools.<ns>.<tool>, text, store, load)."
				}
			},
			"required": ["code"]
		}`),
	}
}

type scriptOptions struct {
	TimeoutMS      int `json:"timeout_ms"`
	MaxOutputBytes int `json:"max_output_bytes"`
}

func parseHeaderOptions(code string) scriptOptions {
	opts := scriptOptions{
		TimeoutMS:      defaultSandboxTimeoutMS,
		MaxOutputBytes: defaultMaxOutputBytes,
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(code), "\n")
	const prefix = "// @options:"
	if strings.HasPrefix(firstLine, prefix) {
		raw := strings.TrimSpace(strings.TrimPrefix(firstLine, prefix))
		_ = json.Unmarshal([]byte(raw), &opts)
	}
	if opts.TimeoutMS <= 0 {
		opts.TimeoutMS = defaultSandboxTimeoutMS
	}
	if opts.TimeoutMS > maxSandboxTimeoutMS {
		opts.TimeoutMS = maxSandboxTimeoutMS
	}
	if opts.MaxOutputBytes <= 0 || opts.MaxOutputBytes > 64*1024 {
		opts.MaxOutputBytes = defaultMaxOutputBytes
	}
	return opts
}

func (t *Tool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, onUpdate tools.UpdateFunc) (tools.Result, error) {
	var input struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rawArgs, &input); err != nil {
		return tools.Result{Output: "invalid codemode arguments: " + err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(input.Code) == "" {
		return tools.Result{Output: "code must not be empty", IsError: true}, nil
	}

	opts := parseHeaderOptions(input.Code)
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(opts.TimeoutMS)*time.Millisecond)
	defer cancel()

	// Fresh isolated zero-privilege goja VM per invocation (NO fs, NO exec, NO network, NO process!)
	vm := goja.New()
	vmTasks := make(chan func(), 128)
	var pendingAsync int

	// Hard preemptive CPU interruption for infinite loops (`while(true){}`)
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-execCtx.Done():
			vm.Interrupt("codemode execution timeout")
		case <-stopWatch:
		}
	}()

	var textOutputs []string
	var extraBlocks []ai.ContentBlock

	_ = vm.Set("text", func(val any) {
		switch v := val.(type) {
		case string:
			textOutputs = append(textOutputs, v)
		default:
			b, _ := json.Marshal(v)
			textOutputs = append(textOutputs, string(b))
		}
	})

	_ = vm.Set("image", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		if len(call.Arguments) >= 2 && call.Arguments[0].ExportType().Kind().String() == "string" {
			mime := call.Arguments[0].String()
			b64 := call.Arguments[1].String()
			if b64 != "" {
				extraBlocks = append(extraBlocks, ai.ImageBase64Block(mime, b64))
			}
			return goja.Undefined()
		}
		exp := call.Arguments[0].Export()
		switch v := exp.(type) {
		case string:
			if strings.HasPrefix(v, "data:") {
				rest := strings.TrimPrefix(v, "data:")
				if semi := strings.Index(rest, ";base64,"); semi > 0 {
					extraBlocks = append(extraBlocks, ai.ImageBase64Block(rest[:semi], rest[semi+len(";base64,"):]))
				}
			}
		case map[string]any:
			mime, _ := v["mime_type"].(string)
			if mime == "" {
				mime, _ = v["mimeType"].(string)
			}
			if mime == "" {
				mime = "image/png"
			}
			b64, _ := v["data_b64"].(string)
			if b64 == "" {
				b64, _ = v["data"].(string)
			}
			if b64 != "" {
				extraBlocks = append(extraBlocks, ai.ImageBase64Block(mime, b64))
			}
		}
		return goja.Undefined()
	})

	// Concurrency semaphore: at most 4 concurrent models.* calls per script.
	modelSem := make(chan struct{}, 4)
	modelsObj := vm.NewObject()
	_ = modelsObj.Set("classify", func(modelRef any, reqVal any) *goja.Promise {
		promise, resolve, reject := vm.NewPromise()
		if t.modelBridge == nil || t.modelBridge.ResolveClassifier == nil {
			_ = reject(vm.ToValue("no classifier model provider configured"))
			return promise
		}
		var modelID string
		switch m := modelRef.(type) {
		case string:
			modelID = m
		case map[string]any:
			modelID, _ = m["id"].(string)
		}
		rawReq, err := json.Marshal(reqVal)
		if err != nil {
			_ = reject(vm.ToValue("invalid classify request: " + err.Error()))
			return promise
		}
		var cReq ai.ClassifyRequest
		if err := json.Unmarshal(rawReq, &cReq); err != nil {
			_ = reject(vm.ToValue("invalid classify request: " + err.Error()))
			return promise
		}
		pendingAsync++
		go func() {
			var resObj any
			var resErr error
			select {
			case modelSem <- struct{}{}:
				defer func() { <-modelSem }()
				prov, resolvedModel, rErr := t.modelBridge.ResolveClassifier(execCtx, modelID)
				if rErr != nil {
					resErr = rErr
				} else {
					cReq.Model = resolvedModel
					resp, cErr := prov.Classify(execCtx, &cReq)
					if cErr != nil {
						resErr = cErr
					} else {
						b, _ := json.Marshal(resp)
						_ = json.Unmarshal(b, &resObj)
					}
				}
			case <-execCtx.Done():
				resErr = execCtx.Err()
			}
			vmTasks <- func() {
				pendingAsync--
				if resErr != nil {
					_ = reject(vm.ToValue(resErr.Error()))
				} else {
					_ = resolve(resObj)
				}
			}
		}()
		return promise
	})
	_ = modelsObj.Set("generateImages", func(modelRef any, reqVal any) *goja.Promise {
		promise, resolve, reject := vm.NewPromise()
		if t.modelBridge == nil || t.modelBridge.ResolveImage == nil {
			_ = reject(vm.ToValue("no image generation provider configured"))
			return promise
		}
		var modelID string
		switch m := modelRef.(type) {
		case string:
			modelID = m
		case map[string]any:
			modelID, _ = m["id"].(string)
		}
		rawReq, err := json.Marshal(reqVal)
		if err != nil {
			_ = reject(vm.ToValue("invalid generateImages request: " + err.Error()))
			return promise
		}
		var iReq ai.ImageRequest
		if err := json.Unmarshal(rawReq, &iReq); err != nil {
			_ = reject(vm.ToValue("invalid generateImages request: " + err.Error()))
			return promise
		}
		pendingAsync++
		go func() {
			var resObj any
			var resErr error
			select {
			case modelSem <- struct{}{}:
				defer func() { <-modelSem }()
				prov, resolvedModel, rErr := t.modelBridge.ResolveImage(execCtx, modelID)
				if rErr != nil {
					resErr = rErr
				} else {
					iReq.Model = resolvedModel
					resp, gErr := prov.GenerateImages(execCtx, &iReq)
					if gErr != nil {
						resErr = gErr
					} else {
						b, _ := json.Marshal(resp)
						_ = json.Unmarshal(b, &resObj)
					}
				}
			case <-execCtx.Done():
				resErr = execCtx.Err()
			}
			vmTasks <- func() {
				pendingAsync--
				if resErr != nil {
					_ = reject(vm.ToValue(resErr.Error()))
				} else {
					_ = resolve(resObj)
				}
			}
		}()
		return promise
	})
	_ = vm.Set("models", modelsObj)

	// Expose `browser` Code-First helpers for batch multi-step browser automation in a single turn.
	browserObj := vm.NewObject()
	invokeBrowser := func(action string, args map[string]any) *goja.Promise {
		promise, resolve, reject := vm.NewPromise()
		if t.browserCaller == nil {
			_ = reject(vm.ToValue("browser is not enabled for this session"))
			return promise
		}
		if args == nil {
			args = map[string]any{}
		}
		pendingAsync++
		go func() {
			out, callErr := t.browserCaller(execCtx, action, args)
			vmTasks <- func() {
				pendingAsync--
				if callErr != nil {
					_ = reject(vm.ToValue(callErr.Error()))
					return
				}
				var parsed any
				if json.Unmarshal([]byte(out), &parsed) == nil {
					_ = resolve(vm.ToValue(parsed))
				} else {
					_ = resolve(vm.ToValue(out))
				}
			}
		}()
		return promise
	}
	toArgMap := func(v goja.Value) map[string]any {
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return map[string]any{}
		}
		if m, ok := v.Export().(map[string]any); ok {
			return m
		}
		return map[string]any{}
	}
	_ = browserObj.Set("call", func(action string, args goja.Value) *goja.Promise {
		return invokeBrowser(action, toArgMap(args))
	})
	_ = browserObj.Set("open", func(url string, opts goja.Value) *goja.Promise {
		m := toArgMap(opts)
		m["url"] = url
		return invokeBrowser("open", m)
	})
	_ = browserObj.Set("snapshot", func(opts goja.Value) *goja.Promise {
		return invokeBrowser("snapshot", toArgMap(opts))
	})
	_ = browserObj.Set("click", func(refOrSelector string, opts goja.Value) *goja.Promise {
		m := toArgMap(opts)
		if strings.HasPrefix(refOrSelector, "e") && len(refOrSelector) >= 2 && refOrSelector[1] >= '0' && refOrSelector[1] <= '9' {
			m["ref"] = refOrSelector
		} else {
			m["selector"] = refOrSelector
		}
		return invokeBrowser("click", m)
	})
	_ = browserObj.Set("fill", func(refOrSelector string, value string, opts goja.Value) *goja.Promise {
		m := toArgMap(opts)
		if strings.HasPrefix(refOrSelector, "e") && len(refOrSelector) >= 2 && refOrSelector[1] >= '0' && refOrSelector[1] <= '9' {
			m["ref"] = refOrSelector
		} else {
			m["selector"] = refOrSelector
		}
		m["value"] = value
		return invokeBrowser("fill", m)
	})
	_ = browserObj.Set("eval", func(expression string) *goja.Promise {
		return invokeBrowser("eval", map[string]any{"expression": expression})
	})
	_ = browserObj.Set("wait", func(opts goja.Value) *goja.Promise {
		return invokeBrowser("wait", toArgMap(opts))
	})
	_ = browserObj.Set("scroll", func(direction string, opts goja.Value) *goja.Promise {
		m := toArgMap(opts)
		if direction != "" {
			m["direction"] = direction
		}
		return invokeBrowser("scroll", m)
	})
	_ = browserObj.Set("getState", func() *goja.Promise {
		return invokeBrowser("get_state", nil)
	})
	_ = vm.Set("browser", browserObj)

	_ = vm.Set("store", func(key string, val any) {
		if err := t.kv.Store(key, val); err != nil {
			panic(vm.ToValue(err.Error()))
		}
	})
	_ = vm.Set("load", func(key string) any {
		return t.kv.Load(key)
	})

	_ = vm.Set("searchTools", func(call goja.FunctionCall) goja.Value {
		p, resolve, _ := vm.NewPromise()
		var query, ns string
		limit := 20
		var rawOpts map[string]any
		if len(call.Arguments) >= 1 && call.Arguments[0] != nil && !goja.IsUndefined(call.Arguments[0]) && !goja.IsNull(call.Arguments[0]) {
			if obj, ok := call.Arguments[0].Export().(map[string]any); ok {
				query, _ = obj["query"].(string)
				rawOpts = obj
			} else {
				query = call.Arguments[0].String()
			}
		}
		if len(call.Arguments) >= 2 && call.Arguments[1] != nil && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			if optsMap, ok := call.Arguments[1].Export().(map[string]any); ok {
				rawOpts = optsMap
			}
		}
		if rawOpts != nil {
			ns, _ = rawOpts["namespace"].(string)
			if l, ok := rawOpts["limit"].(int64); ok {
				limit = int(l)
			} else if lf, ok := rawOpts["limit"].(float64); ok {
				limit = int(lf)
			}
		}
		pendingAsync++
		go func() {
			found := t.pool.SearchToolsCtx(execCtx, query, ns, t.workspacePath, limit)
			vmTasks <- func() {
				pendingAsync--
				_ = resolve(found)
			}
		}()
		return vm.ToValue(p)
	})

	_ = vm.Set("describeTool", func(fullName string) *goja.Promise {
		p, resolve, reject := vm.NewPromise()
		pendingAsync++
		go func() {
			desc, err := t.pool.DescribeToolCtx(execCtx, fullName)
			vmTasks <- func() {
				pendingAsync--
				if err != nil {
					_ = reject(vm.ToValue(err.Error()))
				} else {
					_ = resolve(desc)
				}
			}
		}()
		return p
	})

	// Helper that executes an MCP tool concurrently on a Go goroutine and resolves the Promise on the VM thread!
	invokeMCPAsync := func(namespace, toolName string, argVal goja.Value) *goja.Promise {
		promise, resolve, reject := vm.NewPromise()
		var rawCallArgs json.RawMessage = json.RawMessage(`{}`)
		if argVal != nil && !goja.IsUndefined(argVal) && !goja.IsNull(argVal) {
			if exported := argVal.Export(); exported != nil {
				if b, err := json.Marshal(exported); err == nil {
					rawCallArgs = b
				}
			}
		}

		fullName := namespace + "." + toolName
		entry, found := t.pool.LookupEntry(namespace, toolName)
		readOnly := found && entry.ReadOnly

		pendingAsync++
		go func() {
			start := time.Now()
			if onUpdate != nil {
				onUpdate(fmt.Sprintf("[codemode -> %s started]\n", fullName))
			}

			// Enforce security guard & remote interactive approval (NEVER bypass approvals!)
			if t.authorizer != nil {
				if authErr := t.authorizer(execCtx, fullName, readOnly, rawCallArgs); authErr != nil {
					vmTasks <- func() {
						pendingAsync--
						_ = reject(vm.ToValue(authErr.Error()))
					}
					return
				}
			}

			res, callErr := t.pool.Call(execCtx, namespace, toolName, rawCallArgs)
			if onUpdate != nil {
				onUpdate(fmt.Sprintf("[codemode -> %s finished in %dms]\n", fullName, time.Since(start).Milliseconds()))
			}

			vmTasks <- func() {
				pendingAsync--
				if callErr != nil {
					_ = reject(vm.ToValue(callErr.Error()))
				} else {
					_ = resolve(res)
				}
			}
		}()

		return promise
	}

	_ = vm.Set("callTool", func(fullName string, args goja.Value) *goja.Promise {
		ns, tn, ok := strings.Cut(fullName, ".")
		if !ok {
			p, _, reject := vm.NewPromise()
			_ = reject(vm.ToValue("callTool expects '<namespace>.<tool>'"))
			return p
		}
		return invokeMCPAsync(ns, tn, args)
	})

	// Dynamic two-level `tools.<namespace>.<tool>(args)` proxy via goja.DynamicObject (< 1ms startup!)
	_ = vm.Set("tools", vm.NewDynamicObject(&rootToolsProxy{
		vm:     vm,
		invoke: invokeMCPAsync,
	}))

	wrappedScript := fmt.Sprintf("(async () => {\n%s\n})()", input.Code)
	val, err := vm.RunString(wrappedScript)
	if err != nil {
		return formatSandboxError(err, execCtx), nil
	}

	rootPromise, ok := val.Export().(*goja.Promise)
	if !ok {
		return tools.Result{Output: formatSandboxResult(val.Export(), textOutputs, opts.MaxOutputBytes), ExtraBlocks: extraBlocks}, nil
	}

	// Drive single-threaded JS microtask loop as concurrent Go MCP calls complete
	for rootPromise.State() == goja.PromiseStatePending && pendingAsync > 0 {
		select {
		case fn := <-vmTasks:
			fn()
		case <-execCtx.Done():
			return formatSandboxError(execCtx.Err(), execCtx), nil
		}
	}

	if rootPromise.State() == goja.PromiseStateRejected {
		rej := rootPromise.Result()
		msg := "codemode promise rejected"
		if rej != nil {
			msg = fmt.Sprintf("codemode error: %v", rej.Export())
		}
		return tools.Result{Output: msg, IsError: true}, nil
	}

	var retVal any
	if rootPromise.State() == goja.PromiseStateFulfilled && rootPromise.Result() != nil && !goja.IsUndefined(rootPromise.Result()) {
		retVal = rootPromise.Result().Export()
	}

	return tools.Result{
		Output:      formatSandboxResult(retVal, textOutputs, opts.MaxOutputBytes),
		ExtraBlocks: extraBlocks,
	}, nil
}

type rootToolsProxy struct {
	vm     *goja.Runtime
	invoke func(ns, tool string, args goja.Value) *goja.Promise
}

func (r *rootToolsProxy) Get(ns string) goja.Value {
	return r.vm.NewDynamicObject(&nsToolsProxy{vm: r.vm, ns: ns, invoke: r.invoke})
}
func (r *rootToolsProxy) Set(string, goja.Value) bool { return false }
func (r *rootToolsProxy) Has(string) bool             { return true }
func (r *rootToolsProxy) Delete(string) bool          { return false }
func (r *rootToolsProxy) Keys() []string              { return nil }

type nsToolsProxy struct {
	vm     *goja.Runtime
	ns     string
	invoke func(ns, tool string, args goja.Value) *goja.Promise
}

func (n *nsToolsProxy) Get(toolName string) goja.Value {
	if toolName == "then" {
		return goja.Undefined()
	}
	return n.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		var arg goja.Value
		if len(call.Arguments) > 0 {
			arg = call.Arguments[0]
		}
		return n.vm.ToValue(n.invoke(n.ns, toolName, arg))
	})
}
func (n *nsToolsProxy) Set(string, goja.Value) bool { return false }
func (n *nsToolsProxy) Has(string) bool             { return true }
func (n *nsToolsProxy) Delete(string) bool          { return false }
func (n *nsToolsProxy) Keys() []string              { return nil }

func formatSandboxError(err error, execCtx context.Context) tools.Result {
	var intr *goja.InterruptedError
	if errors.As(err, &intr) || errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return tools.Result{
			Output:  "codemode interrupted: execution timed out (possible infinite loop or slow MCP call)",
			IsError: true,
		}
	}
	return tools.Result{
		Output:  "codemode runtime error: " + err.Error(),
		IsError: true,
	}
}

func formatSandboxResult(retVal any, textOutputs []string, maxBytes int) string {
	var parts []string
	if len(textOutputs) > 0 {
		parts = append(parts, strings.Join(textOutputs, "\n"))
	}
	if retVal != nil {
		switch v := retVal.(type) {
		case string:
			parts = append(parts, v)
		default:
			b, err := json.MarshalIndent(v, "", "  ")
			if err == nil {
				parts = append(parts, string(b))
			}
		}
	}
	combined := strings.Join(parts, "\n")
	if combined == "" {
		return "(codemode finished with no output)"
	}
	if len(combined) > maxBytes {
		return combined[:maxBytes] + fmt.Sprintf("\n... [output truncated at %d bytes; filter/summarize further inside your codemode script]", maxBytes)
	}
	return combined
}
