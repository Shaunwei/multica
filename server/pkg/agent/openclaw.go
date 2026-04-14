package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// openclawBlockedArgs are flags hardcoded by the daemon that must not be
// overridden by user-configured custom_args.
var openclawBlockedArgs = map[string]blockedArgMode{
	"--local":      blockedStandalone, // local mode for daemon execution
	"--json":       blockedStandalone, // JSON output for daemon communication
	"--session-id": blockedWithValue,  // managed by daemon for session resumption
	"--message":    blockedWithValue,  // prompt is set by daemon
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

	var args []string
	if !b.cfg.GatewayMode {
		// Embedded (--local) mode: run a fresh local agent instance each time.
		args = []string{"agent", "--local", "--json", "--session-id", sessionID}
	} else {
		// Gateway mode: route through the OpenClaw Gateway so the task runs
		// under the named agent's context (memory, workspace, tools). A named
		// agent isolates multica tasks from the operator's main Discord session.
		args = []string{"agent", "--json", "--session-id", sessionID}
		if b.cfg.AgentName != "" {
			args = append(args, "--agent", b.cfg.AgentName)
		}
	}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%d", int(opts.Timeout.Seconds())))
	}
	args = append(args, filterCustomArgs(opts.CustomArgs, openclawBlockedArgs, b.cfg.Logger)...)
	args = append(args, "--message", prompt)

	cmd := exec.CommandContext(runCtx, execPath, args...)
	b.cfg.Logger.Debug("agent command", "exec", execPath, "args", args)
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

	b.cfg.Logger.Info("openclaw started", "pid", cmd.Process.Pid, "cwd", opts.Cwd, "model", opts.Model, "gateway", b.cfg.GatewayMode, "session", sessionID)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	// Close the JSON pipe when the context is cancelled so the scanner unblocks.
	go func() {
		<-runCtx.Done()
		_ = jsonPipe.Close()
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		scanResult := b.processOutput(jsonPipe, b.cfg.GatewayMode, msgCh)

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

		// Build usage map. OpenClaw doesn't report model per-step, so we
		// attribute all usage to the configured model (or "unknown").
		var usage map[string]TokenUsage
		u := scanResult.usage
		if u.InputTokens > 0 || u.OutputTokens > 0 || u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
			model := opts.Model
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

// ── Event handlers ──

// openclawEventResult holds accumulated state from processing the event stream.
type openclawEventResult struct {
	status    string
	errMsg    string
	output    string
	sessionID string
	usage     TokenUsage
}

// processOutput reads the JSON result from the openclaw subprocess.
//
//   - Local mode: JSON comes on stderr as a single large blob.
//   - Gateway mode: JSON comes on stdout as {runId, status, result:{payloads,meta}}.
//
// Lines that do not parse as a final result are logged as debug.
func (b *openclawBackend) processOutput(r io.Reader, gatewayMode bool, ch chan<- Message) openclawEventResult {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var rawLines []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if result, ok := tryParseOpenclawResult(line, gatewayMode); ok {
			return b.buildOpenclawEventResult(result, ch)
		}
		b.cfg.Logger.Debug("[openclaw:output] " + line)
		rawLines = append(rawLines, line)
	}

	if err := scanner.Err(); err != nil {
		return openclawEventResult{status: "failed", errMsg: fmt.Sprintf("read stdout: %v", err)}
	}

	trimmed := strings.TrimSpace(strings.Join(rawLines, "\n"))
	if trimmed != "" {
		return openclawEventResult{status: "completed", output: trimmed}
	}
	return openclawEventResult{status: "failed", errMsg: "openclaw returned no parseable output"}
}

// openclawGatewayResult is the outer wrapper produced by gateway mode.
type openclawGatewayResult struct {
	RunID   string         `json:"runId"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Result  *openclawResult `json:"result"`
}

func tryParseOpenclawResult(raw string, gatewayMode bool) (openclawResult, bool) {
	// Try each '{' position until we find valid JSON.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		chunk := []byte(raw[i:])
		if gatewayMode {
			// Gateway wraps the result: {runId, status, result:{payloads, meta}}
			var gw openclawGatewayResult
			if err := json.Unmarshal(chunk, &gw); err == nil && gw.RunID != "" && gw.Result != nil {
				return *gw.Result, true
			}
		} else {
			// Local mode: top-level {payloads, meta}
			var result openclawResult
			if err := json.Unmarshal(chunk, &result); err == nil && (result.Payloads != nil || result.Meta.DurationMs > 0) {
				return result, true
			}
		}
	}
	return openclawResult{}, false
}

func (b *openclawBackend) buildOpenclawEventResult(result openclawResult, ch chan<- Message) openclawEventResult {
	var output strings.Builder
	for _, p := range result.Payloads {
		if p.Text != "" {
			if output.Len() > 0 {
				output.WriteString("\n")
			}
			output.WriteString(p.Text)
		}
	}

	var sessionID string
	var usage TokenUsage
	if result.Meta.AgentMeta != nil {
		if sid, ok := result.Meta.AgentMeta["sessionId"].(string); ok {
			sessionID = sid
		}
		if u, ok := result.Meta.AgentMeta["usage"].(map[string]any); ok {
			usage.InputTokens = openclawInt64(u, "input")
			usage.OutputTokens = openclawInt64(u, "output")
			usage.CacheReadTokens = openclawInt64(u, "cacheRead")
			usage.CacheWriteTokens = openclawInt64(u, "cacheWrite")
		}
	}

	if output.Len() > 0 {
		trySend(ch, Message{Type: MessageText, Content: output.String()})
	}

	return openclawEventResult{
		status:    "completed",
		output:    output.String(),
		sessionID: sessionID,
		usage:     usage,
	}
}

// openclawInt64 safely extracts an int64 from a JSON-decoded map value (which
// may be float64 due to Go's JSON number handling).
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

// ── JSON types for `openclaw agent --json` output ──

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
