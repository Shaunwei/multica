package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// openclawBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args.
var openclawBlockedArgs = map[string]blockedArgMode{
	"--local":         blockedStandalone, // local mode for daemon execution
	"--json":          blockedStandalone, // JSON output for daemon communication
	"--session-id":    blockedWithValue,  // managed by daemon for session resumption
	"--message":       blockedWithValue,  // prompt is set by daemon
	"--model":         blockedWithValue,  // openclaw agent does not accept --model; model is bound at registration via `openclaw agents add/update --model`
	"--system-prompt": blockedWithValue,  // openclaw agent does not accept --system-prompt; instructions are injected into --message
}

// openclawBackend implements Backend by spawning `openclaw agent --json
// --session-id <id> --message <prompt>`.
//
// By default it runs in embedded mode (--local), which starts a blank agent
// instance without any persistent context. When GatewayMode is enabled via
// MULTICA_OPENCLAW_GATEWAY=1, the --local flag is omitted and the task is
// routed through the OpenClaw Gateway, giving agents access to the full
// workspace context (memory, skills, tools, identity).
//
// Session lifecycle:
//   - Gateway mode: each task gets an isolated session derived from its workdir
//     (multica-<task-id>). Comment-triggered follow-ups reuse PriorSessionID
//     for continuity within the same issue thread.
//   - Local mode: session IDs are random per-run (no persistence).
type openclawBackend struct {
	cfg Config
}

func (b *openclawBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "openclaw"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("openclaw executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	sessionID := opts.ResumeSessionID
	if sessionID == "" {
		if b.cfg.GatewayMode {
			// Derive a stable per-task session ID from the task workdir so each
			// Multica task gets its own isolated Gateway session. The workdir path
			// is <workspacesRoot>/<task-id>/workdir — two levels up from workdir
			// gives the short task ID.
			prefix := b.cfg.SessionPrefix
			if prefix == "" {
				prefix = "multica"
			}
			if opts.Cwd != "" {
				sessionID = prefix + "-" + filepath.Base(filepath.Dir(opts.Cwd))
			} else {
				sessionID = fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
			}
		} else {
			sessionID = fmt.Sprintf("multica-%d", time.Now().UnixNano())
		}
	}

	args := b.buildArgs(prompt, sessionID, opts, b.cfg.Logger)

	cmd := exec.CommandContext(runCtx, execPath, args...)
	hideAgentWindow(cmd)
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", args)
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	// Pipe direction differs by mode:
	//   --local:  JSON result → stderr, stdout is empty (or informational)
	//   gateway:  JSON result → stdout, stderr has config/plugin warnings
	var jsonPipe io.ReadCloser
	if b.cfg.GatewayMode {
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("openclaw stdout pipe: %w", err)
		}
		cmd.Stderr = newLogWriter(b.cfg.Logger, "[openclaw:stderr] ")
		jsonPipe = stdoutPipe
	} else {
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			cancel()
			return nil, fmt.Errorf("openclaw stderr pipe: %w", err)
		}
		cmd.Stdout = newLogWriter(b.cfg.Logger, "[openclaw:stdout] ")
		jsonPipe = stderrPipe
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start openclaw: %w", err)
	}

	b.cfg.Logger.Info("openclaw started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Close pipe when the context is cancelled so the scanner unblocks.
	go func() {
		<-runCtx.Done()
		_ = jsonPipe.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		scanResult := b.processOutput(jsonPipe, msgCh)

		// Wait for process exit.
		exitErr := cmd.Wait()
		duration := time.Since(startTime)

		if runCtx.Err() == context.DeadlineExceeded {
			scanResult.status = "timeout"
			scanResult.errMsg = fmt.Sprintf("openclaw timed out after %s", timeout)
		} else if runCtx.Err() == context.Canceled {
			scanResult.status = "aborted"
			scanResult.errMsg = "execution cancelled"
		} else if exitErr != nil && scanResult.status == "completed" {
			scanResult.status = "failed"
			scanResult.errMsg = fmt.Sprintf("openclaw exited with error: %v", exitErr)
		}

		b.cfg.Logger.Info("openclaw finished", "pid", cmd.Process.Pid, "status", scanResult.status, "duration", duration.Round(time.Millisecond).String())

		// Build usage map. Prefer the model openclaw reported in
		// `meta.agentMeta.model` (the actual LLM, e.g. `deepseek-chat`).
		// Fall back to opts.Model — which for openclaw is the agent name
		// passed via `--agent`, not a real model identifier — only when
		// the runtime didn't surface its own model. Last resort is the
		// daemon's `unknown` placeholder.
		var usage map[string]TokenUsage
		u := scanResult.usage
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := scanResult.model
			if model == "" {
				model = opts.Model
			}
			if model == "" {
				model = "unknown"
			}
			usage = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:     scanResult.status,
			Output:     scanResult.output,
			Error:      scanResult.errMsg,
			DurationMs: duration.Milliseconds(),
			SessionID:  scanResult.sessionID,
			Usage:      usage,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

// buildOpenclawArgs assembles the argv for a local-mode `openclaw agent` invocation.
// This package-level function always produces --local args (for testing and local mode).
// For gateway-aware arg building, use openclawBackend.buildArgs.
func buildOpenclawArgs(prompt, sessionID string, opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"agent", "--local", "--json", "--session-id", sessionID}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
	}
	customArgs := filterCustomArgs(opts.CustomArgs, openclawBlockedArgs, logger)
	if opts.Model != "" && !customArgsContains(customArgs, "--agent") {
		args = append(args, "--agent", opts.Model)
	}
	args = append(args, customArgs...)
	// Prepend system prompt / agent instructions into the message body.
	// openclaw does not accept --system-prompt; inject inline.
	if opts.SystemPrompt != "" {
		prompt = opts.SystemPrompt + "\n\n" + prompt
	}
	args = append(args, "--message", prompt)
	return args
}

// buildArgs assembles the argv for a gateway-aware `openclaw agent` invocation.
// Gateway mode omits --local; local mode always includes it.
func (b *openclawBackend) buildArgs(prompt, sessionID string, opts ExecOptions, logger *slog.Logger) []string {
	var args []string
	if !b.cfg.GatewayMode {
		// Embedded (--local) mode: run a fresh local agent instance each time.
		return buildOpenclawArgs(prompt, sessionID, opts, logger)
	}
	// Gateway mode: route through the OpenClaw Gateway so the task runs
	// under the named agent's context (memory, workspace, tools). A named
	// agent isolates multica tasks from the operator's main Discord session.
	args = []string{"agent", "--json", "--session-id", sessionID}
	if b.cfg.AgentName != "" {
		args = append(args, "--agent", b.cfg.AgentName)
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, openclawBlockedArgs, logger)...)
	if opts.SystemPrompt != "" {
		prompt = opts.SystemPrompt + "\n\n" + prompt
	}
	args = append(args, "--message", prompt)
	return args
}

// customArgsContains reports whether args contains the given flag
// (either as a standalone token "--flag" or in "--flag=value" form).
func customArgsContains(args []string, flag string) bool {
	prefix := flag + "="
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, prefix) {
			return true
		}
	}
	return false
}

// ── Event handlers ──

// openclawEventResult holds accumulated state from processing the event stream.
type openclawEventResult struct {
	status    string
	errMsg    string
	output    string
	sessionID string
	usage     TokenUsage
	// model is the LLM identifier reported by openclaw in its result blob
	// (`meta.agentMeta.model`). Empty when the run did not emit it (older
	// openclaw versions, partial outputs). Distinct from `opts.Model`,
	// which for the openclaw backend is the openclaw *agent* name passed
	// via `--agent`, not the underlying model.
	model string
}

// processOutput reads the JSON output from the openclaw subprocess.
// Handles both streaming NDJSON events and the legacy single-blob format.
//
//   - Local mode: JSON comes on stderr (mix of log lines + NDJSON events or final blob).
//   - Gateway mode: JSON comes on stdout as streaming NDJSON events or a final blob.
func (b *openclawBackend) processOutput(r io.Reader, ch chan<- Message) openclawEventResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var output strings.Builder
	var sessionID string
	var model string
	var usage TokenUsage
	finalStatus := "completed"
	var finalError string
	gotEvents := false // true if we parsed at least one streaming event or result

	var rawLines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// Try parsing as a streaming NDJSON event first.
		if event, ok := tryParseOpenclawEvent(line); ok {
			gotEvents = true
			if event.SessionID != "" {
				sessionID = event.SessionID
			}
			switch event.Type {
			case "text":
				if event.Text != "" {
					output.WriteString(event.Text)
					trySend(ch, Message{Type: MessageText, Content: event.Text})
				}
			case "tool_use":
				var input map[string]any
				if event.Input != nil {
					_ = json.Unmarshal(event.Input, &input)
				}
				trySend(ch, Message{
					Type:   MessageToolUse,
					Tool:   event.Tool,
					CallID: event.CallID,
					Input:  input,
				})
			case "tool_result":
				trySend(ch, Message{
					Type:   MessageToolResult,
					Tool:   event.Tool,
					CallID: event.CallID,
					Output: event.Text,
				})
			case "error":
				errMsg := event.errorMessage()
				b.cfg.Logger.Warn("openclaw error event", "error", errMsg)
				trySend(ch, Message{Type: MessageError, Content: errMsg})
				finalStatus = "failed"
				finalError = errMsg
			case "lifecycle":
				phase := event.Phase
				if phase == "error" || phase == "failed" || phase == "cancelled" {
					errMsg := event.errorMessage()
					b.cfg.Logger.Warn("openclaw lifecycle failure", "phase", phase, "error", errMsg)
					trySend(ch, Message{Type: MessageError, Content: errMsg})
					finalStatus = "failed"
					finalError = errMsg
				}
			case "step_start":
				trySend(ch, Message{Type: MessageStatus, Status: "running"})
			case "step_finish":
				if event.Usage != nil {
					u := parseOpenclawUsage(event.Usage)
					usage.InputTokens += u.InputTokens
					usage.OutputTokens += u.OutputTokens
					usage.CacheReadTokens += u.CacheReadTokens
					usage.CacheWriteTokens += u.CacheWriteTokens
				}
			}
			continue
		}

		// Try parsing as a final result blob (legacy format or gateway wrapper).
		if result, ok := tryParseOpenclawResult(line, b.cfg.GatewayMode); ok {
			gotEvents = true
			res := b.buildOpenclawEventResult(result, ch, &output)
			if res.sessionID != "" {
				sessionID = res.sessionID
			}
			if res.model != "" {
				model = res.model
			}
			// Prefer usage from the final result if no streaming events reported it.
			u := res.usage
			if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
				usage = u
			}
			continue
		}

		// Not JSON — treat as log line.
		b.cfg.Logger.Debug("[openclaw:output] " + line)
		rawLines = append(rawLines, line)
	}

	if err := scanner.Err(); err != nil {
		return openclawEventResult{status: "failed", errMsg: fmt.Sprintf("read stderr: %v", err)}
	}

	// If we got no events at all, fall back to raw output.
	if !gotEvents {
		// OpenClaw may output pretty-printed (multi-line) JSON. No single line
		// would parse, so try parsing the accumulated output as a whole.
		trimmed := strings.TrimSpace(strings.Join(rawLines, "\n"))
		if trimmed != "" {
			if result, ok := tryParseOpenclawResult(trimmed, b.cfg.GatewayMode); ok {
				return b.buildOpenclawEventResult(result, ch, &output)
			}
			// Log lines may precede the JSON blob. Find the first line that
			// starts with '{' and try parsing from there.
			for i, line := range rawLines {
				if len(line) > 0 && line[0] == '{' {
					candidate := strings.TrimSpace(strings.Join(rawLines[i:], "\n"))
					if result, ok := tryParseOpenclawResult(candidate, b.cfg.GatewayMode); ok {
						return b.buildOpenclawEventResult(result, ch, &output)
					}
					break
				}
			}
			return openclawEventResult{status: "completed", output: trimmed}
		}
		return openclawEventResult{status: "failed", errMsg: "openclaw returned no parseable output"}
	}

	return openclawEventResult{
		status:    finalStatus,
		errMsg:    finalError,
		output:    output.String(),
		sessionID: sessionID,
		usage:     usage,
		model:     model,
	}
}

// tryParseOpenclawEvent attempts to parse a line as a streaming NDJSON event.
// Returns the event and true if the line is a valid event with a known type.
func tryParseOpenclawEvent(line string) (openclawEvent, bool) {
	if len(line) == 0 || line[0] != '{' {
		return openclawEvent{}, false
	}
	var event openclawEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return openclawEvent{}, false
	}
	if event.Type == "" {
		return openclawEvent{}, false
	}
	return event, true
}

// tryParseOpenclawResult attempts to parse a line as a final result blob.
// Only lines starting with '{' are considered — we do not scan for braces at
// arbitrary positions, which avoids false matches on log lines containing JSON fragments.
// Supports both local mode (top-level {payloads,meta}) and gateway wrapper ({runId,status,result}).
func tryParseOpenclawResult(raw string, gatewayMode bool) (openclawResult, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return openclawResult{}, false
	}
	chunk := []byte(raw)
	if gatewayMode {
		// Gateway wraps the result: {runId, status, result:{payloads, meta}}
		var gw openclawGatewayResult
		if err := json.Unmarshal(chunk, &gw); err == nil && gw.RunID != "" && gw.Result != nil {
			return *gw.Result, true
		}
	}
	// Local mode or fallback: top-level {payloads, meta}
	var result openclawResult
	if err := json.Unmarshal(chunk, &result); err == nil && (result.Payloads != nil || result.Meta.DurationMs > 0) {
		return result, true
	}
	return openclawResult{}, false
}

// buildOpenclawEventResult extracts text and metadata from a final result blob.
// Text payloads are appended to the shared output builder and emitted to ch.
func (b *openclawBackend) buildOpenclawEventResult(result openclawResult, ch chan<- Message, output *strings.Builder) openclawEventResult {
	for _, p := range result.Payloads {
		if p.Text != "" {
			output.WriteString(p.Text)
			trySend(ch, Message{Type: MessageText, Content: p.Text})
		}
	}

	var sessionID string
	var model string
	var usage TokenUsage
	if result.Meta.AgentMeta != nil {
		if sid, ok := result.Meta.AgentMeta["sessionId"].(string); ok {
			sessionID = sid
		}
		// `meta.agentMeta.model` is openclaw's true LLM identifier
		// (e.g. "deepseek-chat", "claude-sonnet-4"). Take it as-is.
		if m, ok := result.Meta.AgentMeta["model"].(string); ok {
			model = strings.TrimSpace(m)
		}
		if u, ok := result.Meta.AgentMeta["usage"].(map[string]any); ok {
			usage = parseOpenclawUsage(u)
		}
	}

	return openclawEventResult{
		status:    "completed",
		output:    output.String(),
		sessionID: sessionID,
		usage:     usage,
		model:     model,
	}
}

// parseOpenclawUsage extracts token usage from a map, supporting multiple
// field name conventions used by different OpenClaw versions:
//
//	input / inputTokens / input_tokens
//	output / outputTokens / output_tokens
//	cacheRead / cachedInputTokens / cached_input_tokens / cache_read
//	cacheWrite / cacheCreationInputTokens / cache_creation_input_tokens / cache_write
func parseOpenclawUsage(data map[string]any) TokenUsage {
	return TokenUsage{
		InputTokens:      openclawInt64FirstOf(data, "input", "inputTokens", "input_tokens"),
		OutputTokens:     openclawInt64FirstOf(data, "output", "outputTokens", "output_tokens"),
		CacheReadTokens:  openclawInt64FirstOf(data, "cacheRead", "cachedInputTokens", "cached_input_tokens", "cache_read", "cache_read_input_tokens"),
		CacheWriteTokens: openclawInt64FirstOf(data, "cacheWrite", "cacheCreationInputTokens", "cache_creation_input_tokens", "cache_write"),
	}
}

// openclawInt64FirstOf returns the first non-zero int64 value found under any of the given keys.
func openclawInt64FirstOf(data map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if v := openclawInt64(data, key); v != 0 {
			return v
		}
	}
	return 0
}

// openclawInt64 safely extracts an int64 from a JSON-decoded map value.
func openclawInt64(data map[string]any, key string) int64 {
	v, ok := data[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}

// ── JSON types ──

// openclawGatewayResult is the outer wrapper produced by gateway mode.
type openclawGatewayResult struct {
	RunID   string          `json:"runId"`
	Status  string          `json:"status"`
	Summary string          `json:"summary"`
	Result  *openclawResult `json:"result"`
}

// openclawEvent represents a single streaming NDJSON event from openclaw --json.
type openclawEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId,omitempty"`
	Text      string          `json:"text,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	CallID    string          `json:"callId,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Usage     map[string]any  `json:"usage,omitempty"`
	Phase     string          `json:"phase,omitempty"`
	Error     *openclawError  `json:"error,omitempty"`
	Message   string          `json:"message,omitempty"`
}

func (e openclawEvent) errorMessage() string {
	if e.Error != nil {
		if msg := e.Error.message(); msg != "" {
			return msg
		}
	}
	if e.Text != "" {
		return e.Text
	}
	if e.Message != "" {
		return e.Message
	}
	return "unknown openclaw error"
}

type openclawError struct {
	Name    string             `json:"name,omitempty"`
	Data    *openclawErrorData `json:"data,omitempty"`
	Message string             `json:"message,omitempty"`
}

func (e *openclawError) message() string {
	if e.Data != nil && e.Data.Message != "" {
		return e.Data.Message
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Name != "" {
		return e.Name
	}
	return ""
}

type openclawErrorData struct {
	Message string `json:"message,omitempty"`
}

// openclawResult represents the JSON output from `openclaw agent --json`.
type openclawResult struct {
	Payloads []openclawPayload `json:"payloads"`
	Meta     openclawMeta      `json:"meta"`
}

type openclawPayload struct {
	Text string `json:"text"`
}

type openclawMeta struct {
	DurationMs int64          `json:"durationMs"`
	AgentMeta  map[string]any `json:"agentMeta"`
}
