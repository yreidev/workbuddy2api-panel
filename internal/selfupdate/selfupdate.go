// Package selfupdate 面板一键更新：从本仓库的 GitHub Release 下载当前平台的包，校验后放进数据目录的
// bin/，再优雅停机并 exec 原地重启。程序自带的那份（镜像里的 /app/wb2api）始终不动：
//
//   - 启动时（Launch）数据目录里有比自己新、校验通过的版本就 exec 过去；自带的不比它旧（拉了新镜像），
//     就删掉数据目录里的，以自带的为准。放在数据卷里，重建容器也不会退回旧版。
//   - 新版启动后稳定跑满 StableAfter，或没跑满但走完了优雅停机，才算成功（MarkStable）。连续 maxAttempts
//     次都没做到（启动就崩、端口被占……），自带程序就不再切过去，面板提示已自动退回。
//   - 面板可以一键退回自带版本（Rollback）：删掉数据目录里的版本再重启。
//
// 重启用 exec 原地换成新程序（PID 不变），不依赖 Docker restart policy 或 systemd；Windows 没有 exec，
// 不支持一键更新。
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/changelog"
)

const (
	// DefaultDownloadBase Release 下载地址前缀，后接「版本号/文件名」。
	DefaultDownloadBase = "https://github.com/yreidev/workbuddy2api-panel/releases/download/v"
	// StableAfter 新版启动后跑满这么久才算成功。
	StableAfter = 30 * time.Second

	binName      = "wb2api"
	manifestName = "current.json"
	maxAttempts  = 3

	// 自带程序 exec 到数据目录里的版本时，用环境变量告诉它自带程序在哪、是哪一版（退回和重启要用）。
	envBaseExe     = "WB2A_BASE_EXE"
	envBaseVersion = "WB2A_BASE_VERSION"
)

// 更新进行到哪一步。
const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseRestarting  = "restarting"
	PhaseFailed      = "failed"
)

// ErrBusy 已经在更新或重启。
var ErrBusy = errors.New("正在更新或重启，请稍候")

// Manifest 数据目录里 current.json 的内容：面板装进来的是哪一版、启动情况如何。
type Manifest struct {
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"` // 可执行文件本身的 sha256，启动前核对
	InstalledAt time.Time `json:"installed_at"`
	// Attempts 切过去之后还没稳定运行（MarkStable）的次数，稳定后清零
	Attempts int `json:"attempts"`
	// Stable 这一版稳定运行过；只用来在装好后第一次稳定时记一条日志
	Stable bool `json:"stable"`
	// Failed 自动退回的原因；有值时启动不再切过去，等面板里删掉或装别的版本
	Failed string `json:"failed,omitempty"`
}

// Status 面板展示用的状态。
type Status struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"` // 不支持一键更新的原因
	Base      string `json:"base"`             // 程序自带的版本
	Override  bool   `json:"override"`         // 正在跑面板装到数据目录的版本
	Phase     string `json:"phase"`
	Target    string `json:"target,omitempty"` // 正在更新到 / 退回到的版本
	Done      int64  `json:"done,omitempty"`   // 已下载字节
	Total     int64  `json:"total,omitempty"`  // 安装包大小；不知道时为 0
	Error     string `json:"error,omitempty"`  // 上一次更新失败的原因
	// RolledBack 数据目录里那份没能稳定运行、已自动退回自带版本的说明
	RolledBack string `json:"rolled_back,omitempty"`
	GraceSec   int    `json:"grace_sec"` // 优雅停机最长等待
}

// Updater 一键更新。Launch 创建；main 设好 Restart 和 Grace 后交给面板。
type Updater struct {
	version     string // 正在跑的版本
	dir         string // 数据目录里的 bin/（绝对路径）
	baseExe     string // 自带程序
	baseVersion string
	override    bool   // 正在跑的是数据目录里的版本
	unsupported string // 不支持一键更新的原因；空 = 支持

	// DownloadBase Release 下载地址前缀；空 = DefaultDownloadBase（测试可替换）。
	DownloadBase string
	// Client 下载用；nil = 默认客户端（认 HTTPS_PROXY 等环境变量，重定向只跟 https）。
	Client *http.Client
	// Restart 让网关优雅停机，停完后由 main 调 Reexec。
	Restart func()
	// Grace 优雅停机最长等待，面板确认框里提示用。
	Grace time.Duration

	mu     sync.Mutex
	phase  string
	target string
	done   int64
	total  int64
	err    string
}

// Launch 启动时调用（在读取账号、恢复状态之前）：数据目录里有比自己新、能用的版本就 exec 过去，
// 不返回；否则返回 Updater。dataDir 是数据目录（state.json 所在目录）。
func Launch(dataDir, version string) *Updater {
	u := newUpdater(dataDir, version)
	if u.unsupported == "" && !u.override {
		u.launch()
	}
	return u
}

func newUpdater(dataDir, version string) *Updater {
	dir := filepath.Join(dataDir, "bin")
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	// /proc/self/exe 是解析过符号链接的路径，数据目录是符号链接时要同样解析才对得上
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	u := &Updater{version: version, dir: dir, baseVersion: version, phase: PhaseIdle}
	exe, err := os.Executable()
	if err == nil {
		if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = real
		}
	}
	u.baseExe = exe
	if base := os.Getenv(envBaseExe); err == nil && base != "" && exe == u.binPath() {
		u.override, u.baseExe, u.baseVersion = true, base, os.Getenv(envBaseVersion)
	}
	u.unsupported = unsupportedReason(runtime.GOOS, runtime.GOARCH, err)
	return u
}

// unsupportedReason 这个平台为什么不能一键更新；空 = 可以。
func unsupportedReason(goos, goarch string, exeErr error) string {
	switch {
	case goos == "windows":
		return "Windows 不支持原地重启，请手动升级"
	case goos != "linux" && goos != "darwin", goarch != "amd64" && goarch != "arm64":
		return fmt.Sprintf("Release 里没有 %s/%s 的安装包，请手动升级", goos, goarch)
	case exeErr != nil:
		return fmt.Sprintf("找不到当前程序的路径（%v），请手动升级", exeErr)
	}
	return ""
}

func (u *Updater) binPath() string { return filepath.Join(u.dir, binName) }

// launch 自带程序启动时决定要不要切到数据目录里的版本。
func (u *Updater) launch() {
	m, err := readManifest(u.dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[update] 读取 %s 失败，使用程序自带的 v%s：%v", filepath.Join(u.dir, manifestName), u.version, err)
		}
		return
	}
	if m.Failed != "" {
		return
	}
	if changelog.Compare(m.Version, u.version) <= 0 {
		// 拉了更新的镜像：数据目录里那份已经过时，删掉免得以后误用
		if err := u.removeOverride(); err != nil {
			log.Printf("[update] 程序自带的 v%s 不比面板安装的 v%s 旧，以自带的为准；删除 %s 失败：%v", u.version, m.Version, u.dir, err)
		} else {
			log.Printf("[update] 程序自带的 v%s 不比面板安装的 v%s 旧，以自带的为准，已删掉面板安装的那份", u.version, m.Version)
		}
		return
	}
	if m.Attempts >= maxAttempts {
		u.fail(m, fmt.Sprintf("v%s 连续 %d 次启动后没能稳定运行，已退回程序自带的 v%s", m.Version, m.Attempts, u.version))
		return
	}
	if sum, err := fileSHA256(u.binPath()); err != nil || sum != m.SHA256 {
		u.fail(m, fmt.Sprintf("v%s 的程序文件和安装时的校验值对不上，已退回程序自带的 v%s", m.Version, u.version))
		return
	}
	m.Attempts++
	if err := writeManifest(u.dir, m); err != nil {
		// 记不下启动次数就可能反复崩溃重启，宁可不切
		log.Printf("[update] 写入 %s 失败，使用程序自带的 v%s：%v", filepath.Join(u.dir, manifestName), u.version, err)
		return
	}
	if m.Attempts > 1 {
		log.Printf("[update] 切换到面板安装的 v%s（上次没能稳定运行，第 %d 次尝试）", m.Version, m.Attempts)
	} else {
		log.Printf("[update] 切换到面板安装的 v%s", m.Version)
	}
	env := withoutBaseEnv(os.Environ())
	env = append(env, envBaseExe+"="+u.baseExe, envBaseVersion+"="+u.version)
	err = execFn(u.binPath(), append([]string{u.binPath()}, os.Args[1:]...), env)
	u.fail(m, fmt.Sprintf("启动 v%s 失败（%v），已退回程序自带的 v%s", m.Version, err, u.version))
}

// fail 记下自动退回的原因，以后启动不再切过去。
func (u *Updater) fail(m Manifest, reason string) {
	m.Failed = reason
	if err := writeManifest(u.dir, m); err != nil {
		log.Printf("[update] 写入 %s 失败：%v", filepath.Join(u.dir, manifestName), err)
	}
	log.Printf("[update] %s", reason)
}

// MarkStable 新版已稳定运行（跑满 StableAfter，或走完了优雅停机）：清零启动次数。只有跑着数据目录里的版本时才有事做。
func (u *Updater) MarkStable() {
	if !u.override {
		return
	}
	m, err := readManifest(u.dir)
	if err != nil || m.Version != u.version || (m.Stable && m.Attempts == 0) {
		return
	}
	first := !m.Stable
	m.Attempts, m.Stable = 0, true
	if err := writeManifest(u.dir, m); err != nil {
		log.Printf("[update] 写入 %s 失败：%v", filepath.Join(u.dir, manifestName), err)
		return
	}
	if first {
		log.Printf("[update] v%s 已稳定运行", u.version)
	}
}

// Status 当前状态。
func (u *Updater) Status() Status {
	u.mu.Lock()
	s := Status{
		Supported: u.unsupported == "",
		Reason:    u.unsupported,
		Base:      u.baseVersion,
		Override:  u.override,
		Phase:     u.phase,
		Target:    u.target,
		Done:      u.done,
		Total:     u.total,
		Error:     u.err,
		GraceSec:  int(u.Grace.Round(time.Second) / time.Second),
	}
	u.mu.Unlock()
	if !u.override && s.Supported {
		if m, err := readManifest(u.dir); err == nil && m.Failed != "" {
			s.RolledBack = m.Failed
		}
	}
	return s
}

func (u *Updater) busyLocked() bool {
	return u.phase == PhaseDownloading || u.phase == PhaseVerifying || u.phase == PhaseRestarting
}

func (u *Updater) setPhase(phase string) {
	u.mu.Lock()
	u.phase = phase
	u.mu.Unlock()
}

// Rollback 删掉数据目录里的版本。正跑着它时重启回自带版本（返回 true）；没跑着（如自动退回后
// 留下的那份）就只删文件。
func (u *Updater) Rollback() (restarting bool, err error) {
	if u.unsupported != "" {
		return false, errors.New(u.unsupported)
	}
	u.mu.Lock()
	if u.busyLocked() {
		u.mu.Unlock()
		return false, ErrBusy
	}
	if !u.override {
		u.mu.Unlock()
		if _, err := os.Stat(filepath.Join(u.dir, manifestName)); err != nil {
			return false, errors.New("没有面板安装的版本可以删除")
		}
		return false, u.removeOverride()
	}
	u.phase, u.target, u.err = PhaseRestarting, u.baseVersion, ""
	u.mu.Unlock()
	if err := u.removeOverride(); err != nil {
		u.mu.Lock()
		u.phase, u.target = PhaseIdle, ""
		u.mu.Unlock()
		return false, err
	}
	log.Printf("[update] 退回程序自带的 v%s，正在重启", u.baseVersion)
	go u.restart()
	return true, nil
}

func (u *Updater) restart() {
	if u.Restart == nil {
		u.mu.Lock()
		u.phase, u.err = PhaseFailed, "没有接入重启，请手动重启网关"
		u.mu.Unlock()
		return
	}
	u.Restart()
}

// Reexec 停机完成后原地换成自带程序，由它决定要不要再切到数据目录里的版本。成功时不返回。
func (u *Updater) Reexec() error {
	return execFn(u.baseExe, append([]string{u.baseExe}, os.Args[1:]...), withoutBaseEnv(os.Environ()))
}

func (u *Updater) removeOverride() error {
	for _, name := range []string{binName, manifestName} {
		if err := os.Remove(filepath.Join(u.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func withoutBaseEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, envBaseExe+"=") && !strings.HasPrefix(kv, envBaseVersion+"=") {
			out = append(out, kv)
		}
	}
	return out
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if !changelog.Valid(m.Version) {
		return m, fmt.Errorf("版本号 %q 不合法", m.Version)
	}
	return m, nil
}

// writeManifest 先写临时文件再改名，断电也不会留下半截文件。
func writeManifest(dir string, m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".current-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, manifestName))
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
