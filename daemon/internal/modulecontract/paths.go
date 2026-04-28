package modulecontract

import "path/filepath"

const (
	DefaultModuleDir = "/data/adb/modules/rknnovpn"
	DefaultGroupID   = "23333"

	BinDirName            = "bin"
	ConfigDirName         = "config"
	RenderedConfigDirName = "rendered"
	ScriptsDirName        = "scripts"
	RunDirName            = "run"
	DataDirName           = "data"
	LogDirName            = "logs"
	BackupDirName         = "backup"
	ProfilesDirName       = "profiles"
	ReleasesDirName       = "releases"

	ResetLockName       = "reset.lock"
	ActiveFileName      = "active"
	ManualFlagName      = "manual"
	DaemonPIDFileName   = "daemon.pid"
	SingBoxPIDFileName  = "singbox.pid"
	DaemonSocketName    = "daemon.sock"
	NetChangeLockName   = "net_change.lock"
	EnvSnapshotName     = "env.sh"
	IPTablesRulesName   = "iptables.rules"
	IP6TablesRulesName  = "ip6tables.rules"
	IPTablesBackupName  = "iptables_backup.rules"
	IP6TablesBackupName = "ip6tables_backup.rules"

	RescueResetScriptName = "rescue_reset.sh"
	DNSScriptName         = "dns.sh"
	IPTablesScriptName    = "iptables.sh"
	RoutingScriptName     = "routing.sh"

	EnvModuleDir  = "RKNNOVPN_DIR"
	EnvRunDir     = "RUN_DIR"
	EnvConfigDir  = "CONFIG_DIR"
	EnvScriptsDir = "SCRIPTS_DIR"
	EnvResetLock  = "RESET_LOCK"
	EnvActiveFile = "ACTIVE_FILE"
	EnvManualFlag = "MANUAL_FLAG"
)

type Paths struct {
	ModuleDir string
}

func NewPaths(moduleDir string) Paths {
	if moduleDir == "" {
		moduleDir = DefaultModuleDir
	}
	return Paths{ModuleDir: moduleDir}
}

func (p Paths) Dir() string {
	if p.ModuleDir == "" {
		return DefaultModuleDir
	}
	return p.ModuleDir
}

func (p Paths) BinDir() string            { return filepath.Join(p.Dir(), BinDirName) }
func (p Paths) ConfigDir() string         { return filepath.Join(p.Dir(), ConfigDirName) }
func (p Paths) RenderedConfigDir() string { return filepath.Join(p.ConfigDir(), RenderedConfigDirName) }
func (p Paths) ScriptsDir() string        { return filepath.Join(p.Dir(), ScriptsDirName) }
func (p Paths) RunDir() string            { return filepath.Join(p.Dir(), RunDirName) }
func (p Paths) DataDir() string           { return filepath.Join(p.Dir(), DataDirName) }
func (p Paths) LogDir() string            { return filepath.Join(p.Dir(), LogDirName) }
func (p Paths) BackupDir() string         { return filepath.Join(p.Dir(), BackupDirName) }
func (p Paths) ProfilesDir() string       { return filepath.Join(p.Dir(), ProfilesDirName) }
func (p Paths) ReleasesDir() string       { return filepath.Join(p.Dir(), ReleasesDirName) }

func (p Paths) ResetLock() string      { return filepath.Join(p.RunDir(), ResetLockName) }
func (p Paths) ActiveFile() string     { return filepath.Join(p.RunDir(), ActiveFileName) }
func (p Paths) ManualFlag() string     { return filepath.Join(p.ConfigDir(), ManualFlagName) }
func (p Paths) DaemonPIDFile() string  { return filepath.Join(p.RunDir(), DaemonPIDFileName) }
func (p Paths) SingBoxPIDFile() string { return filepath.Join(p.RunDir(), SingBoxPIDFileName) }
func (p Paths) DaemonSocket() string   { return filepath.Join(p.RunDir(), DaemonSocketName) }

func (p Paths) RescueResetScript() string {
	return filepath.Join(p.ScriptsDir(), RescueResetScriptName)
}
func (p Paths) DNSScript() string      { return filepath.Join(p.ScriptsDir(), DNSScriptName) }
func (p Paths) IPTablesScript() string { return filepath.Join(p.ScriptsDir(), IPTablesScriptName) }
func (p Paths) RoutingScript() string  { return filepath.Join(p.ScriptsDir(), RoutingScriptName) }

func (p Paths) RuntimeSnapshotFiles() []string {
	return []string{
		p.SingBoxPIDFile(),
		p.ActiveFile(),
		filepath.Join(p.RunDir(), NetChangeLockName),
		filepath.Join(p.RunDir(), IPTablesRulesName),
		filepath.Join(p.RunDir(), IP6TablesRulesName),
		filepath.Join(p.RunDir(), IPTablesBackupName),
		filepath.Join(p.RunDir(), IP6TablesBackupName),
		filepath.Join(p.RunDir(), EnvSnapshotName),
	}
}

func (p Paths) BootCleanupMarkers() []string {
	return append([]string{
		p.ActiveFile(),
		p.ResetLock(),
		p.DaemonPIDFile(),
		p.SingBoxPIDFile(),
		p.DaemonSocket(),
	}, p.RuntimeSnapshotFiles()[2:]...)
}

func (p Paths) DaemonRuntimeFiles() []string {
	return []string{
		p.DaemonPIDFile(),
		p.DaemonSocket(),
	}
}

func (p Paths) ScriptEnv() map[string]string {
	return map[string]string{
		EnvModuleDir:  p.Dir(),
		EnvRunDir:     p.RunDir(),
		EnvConfigDir:  p.ConfigDir(),
		EnvScriptsDir: p.ScriptsDir(),
		EnvResetLock:  p.ResetLock(),
		EnvActiveFile: p.ActiveFile(),
		EnvManualFlag: p.ManualFlag(),
	}
}
