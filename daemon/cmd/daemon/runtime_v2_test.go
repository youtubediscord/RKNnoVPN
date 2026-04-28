package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/config"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/core"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/health"
	profiledoc "github.com/youtubediscord/RKNnoVPN/daemon/internal/profile"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/rescue"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

func newTestResetDaemon(t *testing.T, leftovers []string, withRescueScript bool) *daemon {
	t.Helper()
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Proxy.APIPort = 0
	if err := os.MkdirAll(filepath.Join(dataDir, "run"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	scriptsDir := filepath.Join(dataDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dns.sh", "iptables.sh"} {
		if err := os.WriteFile(filepath.Join(scriptsDir, name), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if withRescueScript {
		if err := os.WriteFile(filepath.Join(scriptsDir, "rescue_reset.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	coreMgr := core.NewCoreManager(cfg, dataDir, log.New(os.Stderr, "", 0))
	healthMon := health.NewHealthMonitor(coreMgr, time.Hour, 3, cfg.Proxy.TProxyPort, cfg.Proxy.DNSPort, cfg.Proxy.Mark, cfg.Health.URL, time.Second, nil)
	cfgPath := filepath.Join(dataDir, "config", "config.json")
	return &daemon{
		cfg:                      cfg,
		cfgPath:                  cfgPath,
		profilePath:              profiledoc.Path(cfgPath),
		dataDir:                  dataDir,
		coreMgr:                  coreMgr,
		healthMon:                healthMon,
		rescueMgr:                rescue.NewRescueManager(coreMgr, cfg, dataDir, 3, time.Second, nil),
		collectLeftoversOverride: func(*config.Config) []string { return leftovers },
	}
}

func resetReportStep(report runtimev2.ResetReport, name string) runtimev2.ResetStep {
	for _, step := range report.Steps {
		if step.Name == name {
			return step
		}
	}
	return runtimev2.ResetStep{}
}

func isRuntimeBusyCode(err error, code string) bool {
	var busy *runtimev2.OperationBusyError
	return errors.As(err, &busy) && busy.Code == code
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func statusHasOperation(status runtimev2.Status, kind runtimev2.OperationKind) bool {
	if status.ActiveOperation != nil && status.ActiveOperation.Kind == kind {
		return true
	}
	return status.LastOperation != nil && status.LastOperation.Kind == kind
}

func waitForDaemonRuntimeOperationDone(t *testing.T, orchestrator *runtimev2.Orchestrator, kind runtimev2.OperationKind) runtimev2.Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := orchestrator.Status()
		if status.ActiveOperation == nil && status.LastOperation != nil && status.LastOperation.Kind == kind {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s operation to finish, status=%#v", kind, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
