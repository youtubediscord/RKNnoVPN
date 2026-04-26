package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

const defaultSocket = "/data/adb/privstack/run/daemon.sock"
const maxFrameBytes = 16 * 1024 * 1024

var Version = "v1.7.13"

var commands = map[string]string{
	"backend.status":            "Get v2 backend status",
	"backend.start":             "Start the selected v2 backend",
	"backend.stop":              "Stop the selected v2 backend",
	"backend.restart":           "True restart of the selected v2 backend",
	"backend.reset":             "Reset backend networking with structured step results",
	"backend.applyDesiredState": "Apply persisted v2 desired backend state",
	"diagnostics.health":        "Run v2 health diagnostics",
	"diagnostics.testNodes":     "Run v2 node probes (TCP direct, tunnel delay, DNS bootstrap)",
	"status":                    "Get proxy status",
	"start":                     "Start proxy",
	"stop":                      "Stop proxy",
	"reload":                    "Reload config and restart proxy",
	"network-reset":             "Force-remove PrivStack iptables/DNS/routing rules",
	"network.reset":             "Alias for network-reset",
	"health":                    "Get health check status",
	"audit":                     "Run privacy/security audit",
	"doctor":                    "Collect redacted diagnostics for support",
	"self-check":                "Return concise health/privacy/compatibility summary",
	"self.check":                "Alias for self-check",
	"app.list":                  "List installed apps known to the daemon",
	"app.resolveUid":            "Resolve a UID to package metadata: privctl app.resolveUid '{\"uid\":10123}'",
	"panel-get":                 "Get APK-facing panel state",
	"panel-set":                 "Set APK-facing panel state atomically (use PRIVSTACK_STDIN_PARAMS=1 for stdin payloads)",
	"config-get":                "Get config value: privctl config-get '{\"key\":\"proxy\"}'",
	"config-set":                "Set config value: privctl config-set '{\"key\":\"proxy\",\"value\":{...}}'",
	"config-set-many":           "Set multiple config values atomically: privctl config-set-many '{\"values\":{...},\"reload\":true}'",
	"config-list":               "List config sections",
	"config-import":             "Import full config: privctl config-import '{...}'",
	"config.import":             "Alias for config-import",
	"subscription-fetch":        "Fetch subscription URL via daemon network access",
	"node-test":                 "Test saved nodes: TCP connect + URL delay via sing-box",
	"node.test":                 "Alias for node-test",
	"logs":                      "Get recent log lines: privctl logs '{\"lines\":100}'",
	"version":                   "Get daemon version",
	"update-check":              "Check for updates from GitHub Releases",
	"update-download":           "Download latest module + APK",
	"update-install":            "Install previously downloaded update",
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		printUsage()
		os.Exit(0)
	}

	if _, ok := commands[cmd]; !ok {
		fmt.Fprintf(os.Stderr, "error: unknown command %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}

	// Build JSON-RPC request.
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  cmd,
	}

	// Parse params either from argv or stdin (for large payloads that would
	// otherwise overflow shell/argv limits).
	if raw := readRawParams(os.Args[2:]); raw != "" {
		var params json.RawMessage
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid JSON params: %v\n", err)
			os.Exit(1)
		}
		req["params"] = params
	}

	// Determine socket path.
	socketPath := os.Getenv("PRIVSTACK_SOCKET")
	if socketPath == "" {
		socketPath = defaultSocket
	}

	// Connect to daemon.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot connect to daemon at %s: %v\n", socketPath, err)
		fmt.Fprintf(os.Stderr, "hint: is privd running?\n")
		os.Exit(1)
	}
	defer conn.Close()

	// Send request.
	reqBytes, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: marshal request: %v\n", err)
		os.Exit(1)
	}
	reqBytes = append(reqBytes, '\n')

	if _, err := conn.Write(reqBytes); err != nil {
		fmt.Fprintf(os.Stderr, "error: send request: %v\n", err)
		os.Exit(1)
	}

	// Read response.
	reader := bufio.NewReader(conn)
	line, err := readFrame(reader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read response: %v\n", err)
		os.Exit(1)
	}
	if len(line) == 0 {
		fmt.Fprintf(os.Stderr, "error: daemon closed connection without response\n")
		os.Exit(1)
	}

	// Parse response.
	var resp struct {
		JSONRPC string           `json:"jsonrpc"`
		ID      int              `json:"id"`
		Result  *json.RawMessage `json:"result,omitempty"`
		Error   *struct {
			Code    int              `json:"code"`
			Message string           `json:"message"`
			Data    *json.RawMessage `json:"data,omitempty"`
		} `json:"error,omitempty"`
	}

	if err := json.Unmarshal(line, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "error: parse response: %v\n", err)
		fmt.Fprintf(os.Stderr, "raw: %s\n", string(line))
		os.Exit(1)
	}

	// Handle error response.
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "error [%d]: %s\n", resp.Error.Code, resp.Error.Message)
		if resp.Error.Data != nil {
			prettyPrint(*resp.Error.Data)
		}
		os.Exit(1)
	}

	// Print result.
	if resp.Result != nil {
		prettyPrint(*resp.Result)
	} else {
		fmt.Println("ok")
	}
}

func prettyPrint(data json.RawMessage) {
	var obj interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		fmt.Println(string(data))
		return
	}

	pretty, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		fmt.Println(string(data))
		return
	}
	fmt.Println(string(pretty))
}

func printUsage() {
	fmt.Println("privctl - PrivStack daemon control CLI")
	fmt.Println()
	fmt.Println("Usage: privctl <command> [json_params]")
	fmt.Println()
	fmt.Println("Commands:")

	// Calculate max command name length for alignment.
	maxLen := 0
	for cmd := range commands {
		if len(cmd) > maxLen {
			maxLen = len(cmd)
		}
	}

	order := []string{
		"backend.status", "backend.start", "backend.stop", "backend.restart", "backend.reset", "backend.applyDesiredState",
		"diagnostics.health", "diagnostics.testNodes",
		"status", "start", "stop", "reload", "network-reset", "network.reset", "health", "audit", "doctor", "self-check", "self.check",
		"app.list", "app.resolveUid",
		"panel-get", "panel-set",
		"config-get", "config-set", "config-set-many", "config-list", "config-import", "config.import", "subscription-fetch", "node-test", "node.test",
		"logs", "version",
		"update-check", "update-download", "update-install",
	}
	for _, cmd := range order {
		desc := commands[cmd]
		fmt.Printf("  %-*s  %s\n", maxLen, cmd, desc)
	}

	fmt.Println()
	fmt.Println("Environment:")
	fmt.Printf("  PRIVSTACK_SOCKET  daemon socket path (default: %s)\n", defaultSocket)
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  privctl status")
	fmt.Println("  privctl start")
	fmt.Println("  privctl audit")
	fmt.Println("  privctl doctor")
	fmt.Println("  privctl self-check")
	fmt.Println("  printf '{\"panel\":{\"id\":\"default\",\"name\":\"Default\"},\"reload\":false}\\n' | PRIVSTACK_STDIN_PARAMS=1 privctl panel-set")
	fmt.Println("  privctl app.resolveUid '{\"uid\":10123}'")
	fmt.Println("  privctl config-get '{\"key\":\"proxy\"}'")
	fmt.Println("  privctl config-set '{\"key\":\"autostart\",\"value\":false}'")
	fmt.Println("  privctl node-test '{\"url\":\"https://www.gstatic.com/generate_204\"}'")
	fmt.Println("  privctl logs '{\"lines\":100}'")
}

func readRawParams(args []string) string {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " "))
	}
	if os.Getenv("PRIVSTACK_STDIN_PARAMS") != "1" {
		return ""
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	var frame []byte
	for {
		chunk, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF && len(frame) > 0 {
				return frame, nil
			}
			return nil, err
		}
		if len(frame)+len(chunk) > maxFrameBytes {
			return nil, fmt.Errorf("frame exceeds %d bytes", maxFrameBytes)
		}
		frame = append(frame, chunk...)
		if !isPrefix {
			return frame, nil
		}
	}
}
