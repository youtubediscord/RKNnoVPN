package resetcontroller

import "github.com/youtubediscord/RKNnoVPN/daemon/internal/modulecontract"

type Paths struct {
	DataDir string
}

func (p Paths) modulePaths() modulecontract.Paths {
	return modulecontract.NewPaths(p.DataDir)
}

func (p Paths) RunDir() string {
	return p.modulePaths().RunDir()
}

func (p Paths) ConfigDir() string {
	return p.modulePaths().ConfigDir()
}

func (p Paths) ScriptsDir() string {
	return p.modulePaths().ScriptsDir()
}

func (p Paths) ResetLock() string {
	return p.modulePaths().ResetLock()
}

func (p Paths) ActiveMarker() string {
	return p.modulePaths().ActiveFile()
}

func (p Paths) ManualFlag() string {
	return p.modulePaths().ManualFlag()
}

func (p Paths) RescueResetScript() string {
	return p.modulePaths().RescueResetScript()
}

func (p Paths) StaleRuntimeFiles() []string {
	return p.modulePaths().RuntimeSnapshotFiles()
}
