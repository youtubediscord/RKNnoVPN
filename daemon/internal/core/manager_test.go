package core

import (
	"errors"
	"testing"
)

func TestIgnorableCleanupScriptError(t *testing.T) {
	if !ignorableCleanupScriptError(errors.New("script not found: /data/adb/privstack/scripts/dns.sh: no such file or directory")) {
		t.Fatal("missing cleanup script should be treated as an idempotent cleanup no-op")
	}
	if ignorableCleanupScriptError(errors.New("exec iptables.sh stop: exit status 2")) {
		t.Fatal("real cleanup command failures must still be reported")
	}
}

func TestRKNHarderingIsBuiltInAlwaysDirect(t *testing.T) {
	if !IsBuiltInAlwaysDirectPackage("com.notcvnt.rknhardering") {
		t.Fatal("RKNHardering self-test app must stay direct by default")
	}
}
