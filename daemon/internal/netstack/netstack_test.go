package netstack

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplyRunsCleanupThenStartsRulesAndDNS(t *testing.T) {
	var calls []string
	manager := New("/data/adb/privstack", map[string]string{"A": "B"}, func(scriptPath string, command string, env map[string]string) error {
		calls = append(calls, filepath.Base(scriptPath)+":"+command+":"+env["A"])
		return nil
	})

	report := manager.Apply()
	if err := report.Err(); err != nil {
		t.Fatalf("apply should succeed: %v %#v", err, report)
	}
	want := []string{
		"dns.sh:stop:B",
		"iptables.sh:stop:B",
		"iptables.sh:start:B",
		"dns.sh:start:B",
	}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected calls: got %#v want %#v", calls, want)
	}
}

func TestApplyDNSFailureRollsBackAndReturnsDNSCode(t *testing.T) {
	var calls []string
	manager := New("/data/adb/privstack", nil, func(scriptPath string, command string, env map[string]string) error {
		call := filepath.Base(scriptPath) + ":" + command
		calls = append(calls, call)
		if call == "dns.sh:start" {
			return errors.New("dns failed")
		}
		return nil
	})

	report := manager.Apply()
	err := report.Err()
	if err == nil {
		t.Fatalf("expected apply error: %#v", report)
	}
	netErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected netstack Error, got %T", err)
	}
	if netErr.Code != "DNS_APPLY_FAILED" {
		t.Fatalf("expected DNS_APPLY_FAILED, got %#v", netErr)
	}
	if !report.RollbackApplied {
		t.Fatalf("expected rollback flag: %#v", report)
	}
	if got := strings.Join(calls, ","); !strings.Contains(got, "iptables.sh:stop") {
		t.Fatalf("expected rollback cleanup, got calls %s", got)
	}
}

func TestCleanupTreatsMissingScriptsAsAlreadyClean(t *testing.T) {
	manager := New("/data/adb/privstack", nil, func(scriptPath string, command string, env map[string]string) error {
		return errors.New("script not found: " + scriptPath + ": no such file or directory")
	})

	report := manager.Cleanup()
	if err := report.Err(); err != nil {
		t.Fatalf("missing cleanup scripts should be no-op: %v %#v", err, report)
	}
	for _, step := range report.Steps {
		if step.Status != "already_clean" {
			t.Fatalf("expected already_clean step, got %#v", report)
		}
	}
}

func TestVerifyCleanupReportsPrivStackRulesAndRoutes(t *testing.T) {
	manager := New("/data/adb/privstack", map[string]string{
		"FWMARK":         "0x2023",
		"ROUTE_TABLE":    "2023",
		"ROUTE_TABLE_V6": "2024",
	}, nil).WithExecCommand(func(name string, args ...string) (string, error) {
		key := name + " " + strings.Join(args, " ")
		switch key {
		case "iptables -w 100 -t mangle -S":
			return "-N PRIVSTACK_OUT\n-A OUTPUT -j PRIVSTACK_OUT", nil
		case "ip rule show":
			return "100: from all fwmark 0x2023 lookup 2023", nil
		case "ip route show table 2023":
			return "local default dev lo scope host", nil
		case "pidof sing-box":
			return "", nil
		default:
			return "", nil
		}
	})

	report := manager.VerifyCleanup()
	if report.Status != "partial" {
		t.Fatalf("expected partial verify report, got %#v", report)
	}
	text := strings.Join(report.Leftovers, "\n")
	for _, want := range []string{
		"iptables mangle rule remains",
		"ip rule remains",
		"ip route table 2023 still has routes",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected leftover %q in %s", want, text)
		}
	}
}

func TestVerifyCleanupIgnoresMissingOptionalCommandAndTables(t *testing.T) {
	manager := New("/data/adb/privstack", map[string]string{
		"FWMARK":      "0x2023",
		"ROUTE_TABLE": "2023",
	}, nil).WithExecCommand(func(name string, args ...string) (string, error) {
		if strings.HasPrefix(name, "iptables") || strings.HasPrefix(name, "ip6tables") {
			return "table does not exist", errors.New("exit status 1")
		}
		if name == "ip" {
			return "", nil
		}
		return "", nil
	})

	report := manager.VerifyCleanup()
	if len(report.Leftovers) != 0 {
		t.Fatalf("missing optional tables should not be leftovers: %#v", report)
	}
	if report.Status != "ok" {
		t.Fatalf("expected ok verify report, got %#v", report)
	}
}

func TestVerifyReturnsVerifyCode(t *testing.T) {
	manager := New("/data/adb/privstack", nil, func(scriptPath string, command string, env map[string]string) error {
		if filepath.Base(scriptPath) == "dns.sh" && command == "status" {
			return errors.New("dns hook missing")
		}
		return nil
	})

	report := manager.Verify()
	err := report.Err()
	if err == nil {
		t.Fatalf("expected verify error: %#v", report)
	}
	netErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected netstack Error, got %T", err)
	}
	if netErr.Code != "NETSTACK_VERIFY_FAILED" {
		t.Fatalf("expected NETSTACK_VERIFY_FAILED, got %#v", netErr)
	}
}

func TestVerifyRunsRuleAndDNSStatus(t *testing.T) {
	var calls []string
	manager := New("/data/adb/privstack", map[string]string{"A": "B"}, func(scriptPath string, command string, env map[string]string) error {
		calls = append(calls, filepath.Base(scriptPath)+":"+command+":"+env["A"])
		return nil
	})

	report := manager.Verify()
	if err := report.Err(); err != nil {
		t.Fatalf("verify should succeed: %v %#v", err, report)
	}
	want := []string{
		"iptables.sh:status:B",
		"dns.sh:status:B",
	}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected calls: got %#v want %#v", calls, want)
	}
}

func TestEffectiveLocalPortsSkipsDisabledHelpersAndAPI(t *testing.T) {
	manager := New("/data/adb/privstack", map[string]string{
		"TPROXY_PORT": "10853",
		"DNS_PORT":    "10856",
		"API_PORT":    "0",
		"SOCKS_PORT":  "0",
		"HTTP_PORT":   "0",
	}, nil)

	got := manager.effectiveLocalPorts()
	if strings.Join(intsToStrings(got), ",") != "10853,10856" {
		t.Fatalf("expected only tproxy/dns ports by default, got %#v", got)
	}
}

func intsToStrings(values []int) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, strconv.Itoa(value))
	}
	return result
}
