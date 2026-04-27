package main

import (
	"encoding/json"
	"fmt"

	appcatalog "github.com/youtubediscord/RKNnoVPN/daemon/internal/apps"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/control"
	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleAppList(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	apps, err := appcatalog.LoadInstalled(appcatalog.DefaultPackagesListPath)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}
	return apps, nil
}

func (d *daemon) handleResolveUID(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	request, err := control.DecodeResolveUIDParams(params)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: err.Error(),
		}
	}

	apps, err := appcatalog.LoadInstalled(appcatalog.DefaultPackagesListPath)
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}

	if app, ok := appcatalog.ResolveUID(apps, request.UID); ok {
		return app, nil
	}

	return nil, &ipc.RPCError{
		Code:    ipc.CodeInvalidParams,
		Message: fmt.Sprintf("no package found for uid %d", request.UID),
	}
}
