package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/nex-harness/nex/pkg/agent"
	"github.com/nex-harness/nex/pkg/ai"
	"github.com/nex-harness/nex/pkg/harness/tools"
	"github.com/nex-harness/nex/pkg/protocol"
)

// UIBridge bridges headless Runner plugin `ctx.ui.*` calls to connected Web/Desktop/Mobile clients.
type UIBridge interface {
	HasUI() bool
	Prompt(ctx context.Context, req protocol.UIPromptRequestPayload) (protocol.UIResolvePayload, error)
	SetStatus(ctx context.Context, key, badge string, widget any)
	SendUserMessage(text string)
}

// NativeExtension is Layer 1: compile-time Go extension interface.
type NativeExtension interface {
	ID() string
	Register(reg *Registry) error
}

// SlashCommand describes a `/command` registered by a Go or TS plugin.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Handler     func(ctx context.Context, args string) error `json:"-"`
}

// Registry collects hooks, custom tools, and slash commands across both Go and TS plugins for a Session.
type Registry struct {
	mu            sync.Mutex
	workDir       string
	ui            UIBridge
	stateKV       map[string]any
	contextHooks  []func(ctx context.Context, msgs []ai.Message) ([]ai.Message, error)
	toolCallHooks []func(ctx context.Context, toolName string, args json.RawMessage) (agent.ToolCallDecision, error)
	toolResHooks  []func(ctx context.Context, toolName string, args json.RawMessage, res tools.Result) (agent.ToolResultMutation, error)
	customTools   []tools.Tool
	commands      map[string]SlashCommand
}

// NewRegistry initializes a per-session extension registry.
func NewRegistry(workDir string, ui UIBridge) *Registry {
	return &Registry{
		workDir:  workDir,
		ui:       ui,
		stateKV:  make(map[string]any),
		commands: make(map[string]SlashCommand),
	}
}

func (r *Registry) OnContextBuildHook(fn func(ctx context.Context, msgs []ai.Message) ([]ai.Message, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextHooks = append(r.contextHooks, fn)
}

func (r *Registry) OnToolCallHook(fn func(ctx context.Context, toolName string, args json.RawMessage) (agent.ToolCallDecision, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolCallHooks = append(r.toolCallHooks, fn)
}

func (r *Registry) OnToolResultHook(fn func(ctx context.Context, toolName string, args json.RawMessage, res tools.Result) (agent.ToolResultMutation, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolResHooks = append(r.toolResHooks, fn)
}

func (r *Registry) RegisterTool(t tools.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.customTools = append(r.customTools, t)
}

func (r *Registry) RegisterCommand(cmd SlashCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := strings.TrimPrefix(strings.TrimSpace(cmd.Name), "/")
	cmd.Name = name
	r.commands[name] = cmd
}

func (r *Registry) Tools() []tools.Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]tools.Tool, len(r.customTools))
	copy(cp, r.customTools)
	return cp
}

func (r *Registry) Commands() []SlashCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SlashCommand, 0, len(r.commands))
	for _, c := range r.commands {
		out = append(out, c)
	}
	return out
}

func (r *Registry) ExecuteCommand(ctx context.Context, name, args string) error {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/")
	r.mu.Lock()
	cmd, ok := r.commands[name]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown slash command /%s", name)
	}
	return cmd.Handler(ctx, args)
}

// Implement agent.Hooks interface on *Registry

func (r *Registry) OnContextBuild(ctx context.Context, messages []ai.Message) ([]ai.Message, error) {
	cur := messages
	for _, fn := range r.contextHooks {
		next, err := fn(ctx, cur)
		if err != nil {
			return cur, err
		}
		cur = next
	}
	return cur, nil
}

func (r *Registry) OnToolCall(ctx context.Context, toolName string, args json.RawMessage) (agent.ToolCallDecision, error) {
	curArgs := args
	for _, fn := range r.toolCallHooks {
		dec, err := fn(ctx, toolName, curArgs)
		if err != nil {
			return agent.ToolCallDecision{}, err
		}
		if dec.Block {
			return dec, nil
		}
		if len(dec.UpdatedArgs) > 0 {
			curArgs = dec.UpdatedArgs
		}
	}
	return agent.ToolCallDecision{UpdatedArgs: curArgs}, nil
}

func (r *Registry) OnToolResult(ctx context.Context, toolName string, args json.RawMessage, res tools.Result) (agent.ToolResultMutation, error) {
	var combined agent.ToolResultMutation
	curRes := res
	for _, fn := range r.toolResHooks {
		mut, err := fn(ctx, toolName, args, curRes)
		if err != nil {
			continue
		}
		if mut.ReplaceOutput != nil {
			combined.ReplaceOutput = mut.ReplaceOutput
			curRes.Output = *mut.ReplaceOutput
		}
		if mut.AppendText != "" {
			combined.AppendText += mut.AppendText
			curRes.Output += mut.AppendText
		}
	}
	return combined, nil
}

// LoadTSPlugin transpiles TypeScript (if compiledJS is empty) and mounts a Layer-2 dynamic plugin inside a dedicated serialized goja VM!
func (r *Registry) LoadTSPlugin(pluginName, sourceTS, compiledJS string) error {
	jsCode := compiledJS
	if strings.TrimSpace(jsCode) == "" {
		tr := api.Transform(sourceTS, api.TransformOptions{
			Loader: api.LoaderTS,
			Format: api.FormatCommonJS,
			Target: api.ES2020,
		})
		if len(tr.Errors) > 0 {
			return fmt.Errorf("plugin %s transpile failed: %s", pluginName, tr.Errors[0].Text)
		}
		jsCode = string(tr.Code)
	}

	var vmMu sync.Mutex
	vm := goja.New()
	moduleObj := vm.NewObject()
	exportsObj := vm.NewObject()
	_ = moduleObj.Set("exports", exportsObj)
	_ = vm.Set("module", moduleObj)
	_ = vm.Set("exports", exportsObj)

	buildPluginCtx := func(ctx context.Context) *goja.Object {
		ctxObj := vm.NewObject()
		hasUI := r.ui != nil && r.ui.HasUI()

		// ctx.exec(cmd, args)
		_ = ctxObj.Set("exec", func(bin string, args []string) map[string]any {
			c := exec.CommandContext(ctx, bin, args...)
			if r.workDir != "" {
				c.Dir = r.workDir
			}
			var stdout, stderr bytes.Buffer
			c.Stdout = &stdout
			c.Stderr = &stderr
			err := c.Run()
			code := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				} else {
					code = -1
				}
			}
			return map[string]any{
				"exitCode": code,
				"stdout":   stdout.String(),
				"stderr":   stderr.String(),
			}
		})

		// ctx.state.get / set
		stateObj := vm.NewObject()
		_ = stateObj.Set("get", func(key string) any {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.stateKV[pluginName+":"+key]
		})
		_ = stateObj.Set("set", func(key string, val any) {
			r.mu.Lock()
			r.stateKV[pluginName+":"+key] = val
			r.mu.Unlock()
		})
		_ = ctxObj.Set("state", stateObj)

		_ = ctxObj.Set("sendUserMessage", func(text string) {
			if r.ui != nil {
				r.ui.SendUserMessage(text)
			}
		})

		// Remote declarative ctx.ui.*
		uiObj := vm.NewObject()
		_ = uiObj.Set("hasUI", hasUI)
		_ = uiObj.Set("select", func(title string, options []string, defaultVal string) any {
			if !hasUI {
				if defaultVal != "" {
					return defaultVal
				}
				return nil
			}
			res, err := r.ui.Prompt(ctx, protocol.UIPromptRequestPayload{
				Kind: "select", Title: title, Options: options, DefaultValue: defaultVal,
			})
			if err != nil || res.Canceled {
				return nil
			}
			return res.Value
		})
		_ = uiObj.Set("confirm", func(title, message string) bool {
			if !hasUI {
				return false
			}
			res, err := r.ui.Prompt(ctx, protocol.UIPromptRequestPayload{
				Kind: "confirm", Title: title, Message: message,
			})
			return err == nil && !res.Canceled && res.Confirmed
		})
		_ = uiObj.Set("input", func(title, placeholder string) any {
			if !hasUI {
				return nil
			}
			res, err := r.ui.Prompt(ctx, protocol.UIPromptRequestPayload{
				Kind: "input", Title: title, Placeholder: placeholder,
			})
			if err != nil || res.Canceled {
				return nil
			}
			return res.Value
		})
		_ = uiObj.Set("setStatus", func(key, badge string) {
			if r.ui != nil {
				r.ui.SetStatus(ctx, key, badge, nil)
			}
		})
		_ = uiObj.Set("setWidget", func(key string, widget any) {
			if r.ui != nil {
				r.ui.SetStatus(ctx, key, "", widget)
			}
		})
		_ = ctxObj.Set("ui", uiObj)
		return ctxObj
	}

	nexAPI := vm.NewObject()
	_ = nexAPI.Set("on", func(hookName string, fn goja.Callable) {
		switch hookName {
		case "tool_call":
			r.OnToolCallHook(func(ctx context.Context, toolName string, args json.RawMessage) (agent.ToolCallDecision, error) {
				vmMu.Lock()
				defer vmMu.Unlock()
				var inputMap any
				_ = json.Unmarshal(args, &inputMap)
				ev := map[string]any{"toolName": toolName, "input": inputMap}
				ret, err := invokeJS(vm, fn, vm.ToValue(ev), buildPluginCtx(ctx))
				if err != nil || ret == nil {
					return agent.ToolCallDecision{}, err
				}
				if m, ok := ret.(map[string]any); ok {
					var dec agent.ToolCallDecision
					dec.Block, _ = m["block"].(bool)
					dec.Reason, _ = m["reason"].(string)
					if up, hasUp := m["updatedInput"]; hasUp && up != nil {
						if b, err := json.Marshal(up); err == nil {
							dec.UpdatedArgs = b
						}
					}
					return dec, nil
				}
				return agent.ToolCallDecision{}, nil
			})

		case "tool_result":
			r.OnToolResultHook(func(ctx context.Context, toolName string, args json.RawMessage, res tools.Result) (agent.ToolResultMutation, error) {
				vmMu.Lock()
				defer vmMu.Unlock()
				var inputMap any
				_ = json.Unmarshal(args, &inputMap)
				ev := map[string]any{"toolName": toolName, "input": inputMap, "output": res.Output, "isError": res.IsError}
				ret, err := invokeJS(vm, fn, vm.ToValue(ev), buildPluginCtx(ctx))
				if err != nil || ret == nil {
					return agent.ToolResultMutation{}, nil
				}
				if m, ok := ret.(map[string]any); ok {
					var mut agent.ToolResultMutation
					if ap, ok := m["appendText"].(string); ok {
						mut.AppendText = ap
					}
					if rp, ok := m["replaceOutput"].(string); ok {
						mut.ReplaceOutput = &rp
					}
					return mut, nil
				}
				return agent.ToolResultMutation{}, nil
			})
		}
	})

	_ = nexAPI.Set("registerCommand", func(name string, spec *goja.Object) {
		var desc string
		if dv := spec.Get("description"); dv != nil && !goja.IsUndefined(dv) {
			desc = dv.String()
		}
		handlerVal := spec.Get("handler")
		fn, ok := goja.AssertFunction(handlerVal)
		if !ok {
			return
		}
		r.RegisterCommand(SlashCommand{
			Name:        name,
			Description: desc,
			Handler: func(ctx context.Context, cmdArgs string) error {
				vmMu.Lock()
				defer vmMu.Unlock()
				_, err := invokeJS(vm, fn, vm.ToValue(cmdArgs), buildPluginCtx(ctx))
				return err
			},
		})
	})

	_ = nexAPI.Set("registerTool", func(name string, spec *goja.Object) {
		var desc string
		if dv := spec.Get("description"); dv != nil && !goja.IsUndefined(dv) {
			desc = dv.String()
		}
		schemaBytes := json.RawMessage(`{"type":"object","properties":{}}`)
		if sv := spec.Get("inputSchema"); sv != nil && !goja.IsUndefined(sv) {
			if b, err := json.Marshal(sv.Export()); err == nil {
				schemaBytes = b
			}
		}
		execVal := spec.Get("execute")
		fn, ok := goja.AssertFunction(execVal)
		if !ok {
			return
		}
		r.RegisterTool(&dynamicJSTool{
			name:   name,
			desc:   desc,
			schema: schemaBytes,
			run: func(ctx context.Context, rawArgs json.RawMessage) (tools.Result, error) {
				vmMu.Lock()
				defer vmMu.Unlock()
				var argObj any
				_ = json.Unmarshal(rawArgs, &argObj)
				ret, err := invokeJS(vm, fn, vm.ToValue(argObj), buildPluginCtx(ctx))
				if err != nil {
					return tools.Result{Output: err.Error(), IsError: true}, nil
				}
				if s, ok := ret.(string); ok {
					return tools.Result{Output: s}, nil
				}
				b, _ := json.Marshal(ret)
				return tools.Result{Output: string(b)}, nil
			},
		})
	})

	if _, err := vm.RunString(jsCode); err != nil {
		return fmt.Errorf("plugin %s eval failed: %w", pluginName, err)
	}

	exp := moduleObj.Get("exports")
	var entryFn goja.Callable
	if fn, ok := goja.AssertFunction(exp); ok {
		entryFn = fn
	} else if expObj := exp.ToObject(vm); expObj != nil {
		if def := expObj.Get("default"); def != nil {
			if fn, ok := goja.AssertFunction(def); ok {
				entryFn = fn
			}
		}
	}
	if entryFn == nil {
		return fmt.Errorf("plugin %s must default-export a setup function", pluginName)
	}
	if _, err := entryFn(goja.Undefined(), nexAPI); err != nil {
		return fmt.Errorf("plugin %s init failed: %w", pluginName, err)
	}
	return nil
}

func invokeJS(_ *goja.Runtime, fn goja.Callable, arg1, arg2 goja.Value) (any, error) {
	val, err := fn(goja.Undefined(), arg1, arg2)
	if err != nil {
		return nil, err
	}
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil, nil
	}
	if p, ok := val.Export().(*goja.Promise); ok {
		if p.State() == goja.PromiseStateRejected {
			return nil, fmt.Errorf("plugin promise rejected: %v", p.Result().Export())
		}
		if p.Result() == nil || goja.IsUndefined(p.Result()) {
			return nil, nil
		}
		return p.Result().Export(), nil
	}
	return val.Export(), nil
}

type dynamicJSTool struct {
	name   string
	desc   string
	schema json.RawMessage
	run    func(ctx context.Context, rawArgs json.RawMessage) (tools.Result, error)
}

func (d *dynamicJSTool) Name() string                      { return d.name }
func (d *dynamicJSTool) Category() string                  { return "plugin" }
func (d *dynamicJSTool) IsReadOnly(_ json.RawMessage) bool { return false }
func (d *dynamicJSTool) Definition() ai.ToolDefinition {
	return ai.ToolDefinition{Name: d.name, Category: "plugin", Description: d.desc, InputSchema: d.schema}
}
func (d *dynamicJSTool) Execute(ctx context.Context, _ string, rawArgs json.RawMessage, _ tools.UpdateFunc) (tools.Result, error) {
	return d.run(ctx, rawArgs)
}
