package main

import "github.com/youtubediscord/RKNnoVPN/daemon/internal/core"

func (d *daemon) beginRuntimeStartOperation() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	d.runtimeOpEpoch++
	d.runtimeDesiredRunning = true
	return d.runtimeOpEpoch
}

func (d *daemon) beginRuntimeStopOperation() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	d.runtimeOpEpoch++
	d.runtimeDesiredRunning = false
	return d.runtimeOpEpoch
}

func (d *daemon) markRuntimeStartFailed(epoch uint64) {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	if d.runtimeOpEpoch == epoch {
		d.runtimeDesiredRunning = false
	}
}

func (d *daemon) currentRuntimeOperationEpoch() uint64 {
	d.runtimeOpMu.Lock()
	defer d.runtimeOpMu.Unlock()
	return d.runtimeOpEpoch
}

func (d *daemon) canRunRuntimeRecovery(epoch uint64) bool {
	d.runtimeOpMu.Lock()
	allowed := d.runtimeDesiredRunning && d.runtimeOpEpoch == epoch
	d.runtimeOpMu.Unlock()
	if !allowed {
		return false
	}
	if skip, _ := d.shouldSkipRootReconcile(); skip {
		return false
	}
	state := d.coreMgr.GetState()
	return state == core.StateRunning || state == core.StateDegraded || state == core.StateRescue
}
