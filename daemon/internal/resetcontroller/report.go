package resetcontroller

import "github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"

type StepObserver func(generation int64, name string)

type ReportController struct {
	generation int64
	observer   StepObserver
	report     runtimev2.ResetReport
}

func NewReportController(generation int64, backend runtimev2.BackendKind, observer StepObserver) *ReportController {
	return &ReportController{
		generation: generation,
		observer:   observer,
		report: runtimev2.ResetReport{
			BackendKind: backend,
			Generation:  generation,
			Status:      "ok",
		},
	}
}

func (c *ReportController) Run(name string, fn func() (string, string, error)) {
	if c.observer != nil {
		c.observer(c.generation, name)
	}
	status, detail, err := fn()
	if status == "" {
		status = "ok"
	}
	step := runtimev2.ResetStep{Name: name, Status: status, Detail: detail}
	if err != nil {
		step.Status = "failed"
		step.Detail = err.Error()
		c.report.Status = "partial"
		c.report.Errors = append(c.report.Errors, name+": "+err.Error())
	}
	c.report.Steps = append(c.report.Steps, step)
}

func (c *ReportController) SetLeftovers(leftovers []string) {
	c.report.Leftovers = leftovers
	if len(leftovers) > 0 {
		c.report.RebootRequired = true
	}
}

func (c *ReportController) AddWarning(warning string) {
	c.report.Warnings = append(c.report.Warnings, warning)
}

func (c *ReportController) Finish() runtimev2.ResetReport {
	if len(c.report.Errors) > 0 {
		c.report.Status = "partial"
	} else if len(c.report.Warnings) > 0 || len(c.report.Leftovers) > 0 {
		c.report.Status = "clean_with_warnings"
	}
	if len(c.report.Leftovers) > 0 {
		c.report.RebootRequired = true
	}
	return c.report
}
