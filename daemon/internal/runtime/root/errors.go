package root

import (
	"errors"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

type resetReportError struct {
	err    error
	report runtimev2.ResetReport
}

func RuntimeErrorWithResetReport(err error, report runtimev2.ResetReport) error {
	if err == nil {
		return nil
	}
	return resetReportError{err: err, report: report}
}

func (e resetReportError) Error() string {
	return e.err.Error()
}

func (e resetReportError) Unwrap() error {
	return e.err
}

func (e resetReportError) RuntimeResetReport() runtimev2.ResetReport {
	return e.report
}

func ResetReportFromError(err error) *runtimev2.ResetReport {
	if err == nil {
		return nil
	}
	var withReport interface {
		RuntimeResetReport() runtimev2.ResetReport
	}
	if errors.As(err, &withReport) {
		report := withReport.RuntimeResetReport()
		return &report
	}
	return nil
}
