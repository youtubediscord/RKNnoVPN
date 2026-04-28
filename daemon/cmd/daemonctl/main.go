package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"
)

const maxFrameBytes = 16 * 1024 * 1024

var Version = "v1.8.0"

var commands = map[string]string{
	"backend.status":            "Get v2 backend status",
	"backend.start":             "Start the selected v2 backend",
	"backend.stop":              "Stop the selected v2 backend",
	"backend.restart":           "True restart of the selected v2 backend",
	"backend.reset":             "Reset backend networking with structured step results",
	"backend.applyDesiredState": "Apply persisted v2 desired backend state",
	"diagnostics.health":        "Run v2 health diagnostics",
	"diagnostics.testNodes":     "Run v2 node probes (TCP direct, tunnel delay, DNS bootstrap)",
	"audit":                     "Run privacy/security audit",
	"diagnostics.report":        "Collect redacted diagnostics for support",
	"self-check":                "Return concise health/privacy/compatibility summary",
	"ipc.contract":              "Print the daemon IPC method/capability contract",
	"app.list":                  "List installed apps known to the daemon",
	"app.resolveUid":            "Resolve a UID to package metadata: daemonctl app.resolveUid '{\"uid\":10123}'",
	"profile.get":               "Get daemon-owned profile document",
	"profile.apply":             "Validate, persist, and apply a profile document",
	"profile.importNodes":       "Import already-parsed nodes into the daemon profile",
	"profile.setActiveNode":     "Select a live profile node: daemonctl profile.setActiveNode '{\"nodeId\":\"...\"}'",
	"subscription.preview":      "Fetch and preview subscription merge without writing",
	"subscription.refresh":      "Fetch subscription and apply merge through profile.apply",
	"config-list":               "List config sections",
	"config-import":             "Import full config: daemonctl config-import '{...}'",
	"logs":                      "Get recent log lines: daemonctl logs '{\"lines\":100}'",
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

	if _, ok := supportedCommandSet()[cmd]; !ok {
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
	socketPath := os.Getenv("RKNNOVPN_SOCKET")
	if socketPath == "" {
		socketPath = modulecontract.NewPaths("").DaemonSocket()
	}

	// Connect to daemon.
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot connect to daemon at %s: %v\n", socketPath, err)
		fmt.Fprintf(os.Stderr, "hint: is daemon running?\n")
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
		prettyPrint(line)
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
	fmt.Println("daemonctl - RKNnoVPN daemon control CLI")
	fmt.Println()
	fmt.Println("Usage: daemonctl <command> [json_params]")
	fmt.Println()
	fmt.Println("Commands:")

	// Calculate max command name length for alignment.
	maxLen := 0
	for cmd := range supportedCommandSet() {
		if len(cmd) > maxLen {
			maxLen = len(cmd)
		}
	}

	for _, cmd := range orderedCommands() {
		desc := commandDescription(cmd)
		fmt.Printf("  %-*s  %s\n", maxLen, cmd, desc)
	}

	fmt.Println()
	fmt.Println("Environment:")
	fmt.Printf("  RKNNOVPN_SOCKET  daemon socket path (default: %s)\n", modulecontract.NewPaths("").DaemonSocket())
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  daemonctl backend.status")
	fmt.Println("  daemonctl backend.start")
	fmt.Println("  daemonctl audit")
	fmt.Println("  daemonctl diagnostics.report")
	fmt.Println("  daemonctl self-check")
	fmt.Println("  daemonctl profile.get")
	fmt.Println("  daemonctl profile.setActiveNode '{\"nodeId\":\"node-1\"}'")
	fmt.Println("  daemonctl subscription.preview '{\"url\":\"https://example.com/sub\"}'")
	fmt.Println("  daemonctl app.resolveUid '{\"uid\":10123}'")
	fmt.Println("  daemonctl diagnostics.testNodes '{\"url\":\"https://www.gstatic.com/generate_204\"}'")
	fmt.Println("  daemonctl logs '{\"lines\":100}'")
}

func supportedCommandSet() map[string]bool {
	methods := ipc.SupportedMethods()
	result := make(map[string]bool, len(methods))
	for _, method := range methods {
		result[method] = true
	}
	return result
}

func orderedCommands() []string {
	contracts := ipc.MethodContracts()
	methods := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		methods = append(methods, contract.Method)
	}
	sort.Strings(methods)
	return methods
}

func commandDescription(method string) string {
	if desc := commands[method]; desc != "" {
		return desc
	}
	for _, contract := range ipc.MethodContracts() {
		if contract.Method != method {
			continue
		}
		parts := []string{}
		if contract.Mutating {
			parts = append(parts, "mutating")
		}
		if contract.Async {
			parts = append(parts, "async")
		}
		if contract.Capability != "" {
			parts = append(parts, contract.Capability)
		}
		if contract.Request != "" && contract.Result != "" {
			parts = append(parts, fmt.Sprintf("%s -> %s", contract.Request, contract.Result))
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return "IPC contract method"
}

func readRawParams(args []string) string {
	if len(args) > 0 {
		return strings.TrimSpace(strings.Join(args, " "))
	}
	if os.Getenv("RKNNOVPN_STDIN_PARAMS") != "1" {
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
