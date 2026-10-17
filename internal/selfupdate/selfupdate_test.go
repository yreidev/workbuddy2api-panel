package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeBinary 冒充新版 wb2api 的脚本：-version 时打印 version。
func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\necho " + version + "\n")
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"README.md", []byte("readme")}, {name, body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// release 模拟 GitHub Release 的下载地址：/1.2.0/checksums.txt、/1.2.0/<安装包>。
type release struct {
	version string
	archive []byte
	sums    string // 空 = 按 archive 算
}

func serveRelease(t *testing.T, rel release) *httptest.Server {
	t.Helper()
	asset := AssetName(rel.version, runtime.GOOS, runtime.GOARCH)
	sums := rel.sums
	if sums == "" {
		sums = sha([]byte("other")) + "  wb2api-panel-v" + rel.version + "-windows-amd64.zip\n" + sha(rel.archive) + "  " + asset + "\n"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + rel.version + "/checksums.txt":
			_, _ = w.Write([]byte(sums))
		case "/" + rel.version + "/" + asset:
			_, _ = w.Write(rel.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestUpdater(t *testing.T, version string) *Updater {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不支持一键更新")
	}
	u := newUpdater(t.TempDir(), version)
	if u.unsupported != "" {
		t.Skipf("当前平台不支持：%s", u.unsupported)
	}
	return u
}

// waitPhase 等后台更新走到终态（restarting 或 failed）。
func waitPhase(t *testing.T, u *Updater) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := u.Status(); s.Phase == PhaseRestarting || s.Phase == PhaseFailed {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("更新没有结束：%+v", u.Status())
	return Status{}
}

// leftovers 数据目录里除了 wb2api 和 current.json 之外的文件（临时文件应该都清掉了）。
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != binName && e.Name() != manifestName {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestStartInstallsAndRestarts(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	bin := fakeBinary("1.2.0")
	u.DownloadBase = serveRelease(t, release{version: "1.2.0", archive: tarGz(t, "wb2api", bin)}).URL + "/"
	restarted := make(chan struct{}, 1)
	u.Restart = func() { restarted <- struct{}{} }

	if err := u.Start("1.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := u.Start("1.2.0"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start err=%v, want ErrBusy", err)
	}
	s := waitPhase(t, u)
	if s.Phase != PhaseRestarting || s.Target != "1.2.0" || s.Error != "" || s.Done == 0 || s.Done != s.Total {
		t.Fatalf("status = %+v", s)
	}
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("没有调用 Restart")
	}
	got, err := os.ReadFile(u.binPath())
	if err != nil || !bytes.Equal(got, bin) {
		t.Fatalf("binary = %q, %v", got, err)
	}
	if fi, _ := os.Stat(u.binPath()); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("binary mode = %v, want executable", fi.Mode())
	}
	m, err := readManifest(u.dir)
	if err != nil || m.Version != "1.2.0" || m.SHA256 != sha(bin) || m.Attempts != 0 || m.Failed != "" {
		t.Fatalf("manifest = %+v, %v", m, err)
	}
	if l := leftovers(t, u.dir); len(l) != 0 {
		t.Fatalf("leftover files: %v", l)
	}
}

func TestStartFailures(t *testing.T) {
	good := func(t *testing.T) []byte { return tarGz(t, "wb2api", fakeBinary("1.2.0")) }
	cases := []struct {
		name string
		rel  func(t *testing.T) release
		want string
	}{
		{"没有这一版", func(t *testing.T) release { return release{version: "1.3.0", archive: good(t)} }, "CI 可能还在构建"},
		{"校验值不符", func(t *testing.T) release {
			return release{version: "1.2.0", archive: good(t), sums: sha([]byte("x")) + "  " + AssetName("1.2.0", runtime.GOOS, runtime.GOARCH) + "\n"}
		}, "sha256"},
		{"校验文件里没有", func(t *testing.T) release { return release{version: "1.2.0", archive: good(t), sums: "\n"} }, "checksums.txt 里没有"},
		{"包里没有程序", func(t *testing.T) release {
			return release{version: "1.2.0", archive: tarGz(t, "bin/wb2api", fakeBinary("1.2.0"))}
		}, "没有 wb2api"},
		{"版本对不上", func(t *testing.T) release {
			return release{version: "1.2.0", archive: tarGz(t, "wb2api", fakeBinary("9.9.9"))}
		}, "不是 v1.2.0"},
		{"不能执行", func(t *testing.T) release {
			return release{version: "1.2.0", archive: tarGz(t, "wb2api", []byte("\x7fELF不是这台机器的程序"))}
		}, "试运行新程序失败"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newTestUpdater(t, "1.1.0")
			u.DownloadBase = serveRelease(t, c.rel(t)).URL + "/"
			u.Restart = func() { t.Error("失败时不应该重启") }
			if err := u.Start("1.2.0"); err != nil {
				t.Fatal(err)
			}
			s := waitPhase(t, u)
			if s.Phase != PhaseFailed || !strings.Contains(s.Error, c.want) {
				t.Fatalf("status = %+v, want error containing %q", s, c.want)
			}
			if _, err := os.Stat(u.binPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("失败时不应留下 wb2api：%v", err)
			}
			if l := leftovers(t, u.dir); len(l) != 0 {
				t.Fatalf("leftover files: %v", l)
			}
		})
	}
}

func TestStartRejects(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	for _, v := range []string{"1.1.0", "1.0.9", "v1.2.0", ""} {
		if err := u.Start(v); err == nil {
			t.Fatalf("Start(%q) 应该拒绝", v)
		}
	}
	u.unsupported = "Windows 不支持原地重启，请手动升级"
	if err := u.Start("1.2.0"); err == nil || !strings.Contains(err.Error(), "Windows") {
		t.Fatalf("unsupported Start err=%v", err)
	}
}

type execCall struct {
	path string
	args []string
	env  []string
}

var errExecStub = errors.New("exec stub")

// stubExec 替换 execFn：记下调用。fail 为 nil 时像真的 exec 一样不返回（用 panic 跳出 launch）。
func stubExec(t *testing.T, fail error) *[]execCall {
	t.Helper()
	calls := &[]execCall{}
	old := execFn
	execFn = func(path string, args, env []string) error {
		*calls = append(*calls, execCall{path, args, env})
		if fail != nil {
			return fail
		}
		panic(errExecStub)
	}
	t.Cleanup(func() { execFn = old })
	return calls
}

func runLaunch(u *Updater) {
	defer func() {
		if r := recover(); r != nil && r != errExecStub {
			panic(r)
		}
	}()
	u.launch()
}

// installed 在数据目录里放一份「面板装好的」版本。
func installed(t *testing.T, u *Updater, m Manifest, bin []byte) {
	t.Helper()
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.binPath(), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if m.SHA256 == "" {
		m.SHA256 = sha(bin)
	}
	if err := writeManifest(u.dir, m); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchSwitchesToInstalled(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	installed(t, u, Manifest{Version: "1.2.0"}, fakeBinary("1.2.0"))
	t.Setenv(envBaseExe, "/stale")
	calls := stubExec(t, nil)
	runLaunch(u)

	if len(*calls) != 1 {
		t.Fatalf("exec calls = %d, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.path != u.binPath() || c.args[0] != u.binPath() || !slices.Equal(c.args[1:], os.Args[1:]) {
		t.Fatalf("exec = %s %v", c.path, c.args)
	}
	if !slices.Contains(c.env, envBaseExe+"="+u.baseExe) || !slices.Contains(c.env, envBaseVersion+"=1.1.0") || slices.Contains(c.env, envBaseExe+"=/stale") {
		t.Fatalf("exec env 缺少或重复自带程序的信息")
	}
	if m, _ := readManifest(u.dir); m.Attempts != 1 || m.Stable || m.Failed != "" {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestLaunchSkips(t *testing.T) {
	cases := []struct {
		name       string
		m          Manifest
		corrupt    bool
		execErr    error
		removed    bool   // 文件被删掉
		failedWith string // current.json 记下的退回原因
	}{
		{name: "自带的更新", m: Manifest{Version: "1.1.0"}, removed: true},
		{name: "自带的更新（数据目录里是旧版）", m: Manifest{Version: "1.0.0"}, removed: true},
		{name: "连续失败", m: Manifest{Version: "1.2.0", Attempts: maxAttempts}, failedWith: "连续 3 次"},
		{name: "文件被改过", m: Manifest{Version: "1.2.0"}, corrupt: true, failedWith: "校验值对不上"},
		{name: "已判定失败", m: Manifest{Version: "1.2.0", Failed: "之前的原因"}, failedWith: "之前的原因"},
		{name: "exec 失败", m: Manifest{Version: "1.2.0"}, execErr: errors.New("exec format error"), failedWith: "exec format error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newTestUpdater(t, "1.1.0")
			installed(t, u, c.m, fakeBinary(c.m.Version))
			if c.corrupt {
				if err := os.WriteFile(u.binPath(), []byte("tampered"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			calls := stubExec(t, c.execErr)
			runLaunch(u)
			if c.execErr == nil && len(*calls) != 0 {
				t.Fatalf("不应该 exec：%+v", *calls)
			}
			m, err := readManifest(u.dir)
			if c.removed {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("current.json 应该被删掉：%v", err)
				}
				if _, err := os.Stat(u.binPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("wb2api 应该被删掉：%v", err)
				}
				return
			}
			if !strings.Contains(m.Failed, c.failedWith) {
				t.Fatalf("manifest.Failed = %q, want %q", m.Failed, c.failedWith)
			}
			if s := u.Status(); s.RolledBack != m.Failed {
				t.Fatalf("Status.RolledBack = %q", s.RolledBack)
			}
		})
	}
}

func TestLaunchNothingInstalled(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	calls := stubExec(t, nil)
	runLaunch(u)
	if len(*calls) != 0 {
		t.Fatalf("exec calls = %+v", *calls)
	}
}

func TestMarkStable(t *testing.T) {
	u := newTestUpdater(t, "1.2.0")
	installed(t, u, Manifest{Version: "1.2.0", Attempts: 2}, fakeBinary("1.2.0"))
	u.MarkStable() // 不是跑着数据目录里的版本：不动
	if m, _ := readManifest(u.dir); m.Attempts != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	u.override = true
	u.MarkStable()
	if m, _ := readManifest(u.dir); m.Attempts != 0 || !m.Stable {
		t.Fatalf("manifest = %+v", m)
	}
	// 自带程序（v1.1.0）重启后再次切过去：只累加次数，不清掉 stable（「已稳定运行」只在装好后记一次）
	u.override, u.version = false, "1.1.0"
	stubExec(t, nil)
	runLaunch(u)
	if m, _ := readManifest(u.dir); m.Attempts != 1 || !m.Stable {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestRollback(t *testing.T) {
	t.Run("没有可删的", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		if _, err := u.Rollback(); err == nil {
			t.Fatal("应该报错")
		}
	})
	t.Run("删掉自动退回后留下的", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		installed(t, u, Manifest{Version: "1.2.0", Failed: "没能稳定运行"}, fakeBinary("1.2.0"))
		u.Restart = func() { t.Error("没跑着它，不应该重启") }
		restarting, err := u.Rollback()
		if err != nil || restarting {
			t.Fatalf("Rollback = %v, %v", restarting, err)
		}
		if l, _ := os.ReadDir(u.dir); len(l) != 0 {
			t.Fatalf("数据目录应该清空：%v", l)
		}
		if s := u.Status(); s.RolledBack != "" {
			t.Fatalf("RolledBack = %q", s.RolledBack)
		}
	})
	t.Run("退回自带版本", func(t *testing.T) {
		u := newTestUpdater(t, "1.2.0")
		installed(t, u, Manifest{Version: "1.2.0"}, fakeBinary("1.2.0"))
		u.override, u.baseVersion = true, "1.1.0"
		restarted := make(chan struct{}, 1)
		u.Restart = func() { restarted <- struct{}{} }
		restarting, err := u.Rollback()
		if err != nil || !restarting {
			t.Fatalf("Rollback = %v, %v", restarting, err)
		}
		select {
		case <-restarted:
		case <-time.After(5 * time.Second):
			t.Fatal("没有调用 Restart")
		}
		if s := u.Status(); s.Phase != PhaseRestarting || s.Target != "1.1.0" {
			t.Fatalf("status = %+v", s)
		}
		if _, err := os.Stat(u.binPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("wb2api 应该被删掉：%v", err)
		}
		if _, err := u.Rollback(); !errors.Is(err, ErrBusy) {
			t.Fatalf("重启中再退回 err=%v", err)
		}
	})
}

func TestReexecStripsBaseEnv(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	u.baseExe = "/app/wb2api"
	t.Setenv(envBaseExe, "/app/wb2api")
	t.Setenv(envBaseVersion, "1.0.0")
	calls := stubExec(t, errors.New("stop"))
	_ = u.Reexec()
	c := (*calls)[0]
	if c.path != "/app/wb2api" || c.args[0] != "/app/wb2api" {
		t.Fatalf("exec = %s %v", c.path, c.args)
	}
	for _, kv := range c.env {
		if strings.HasPrefix(kv, "WB2A_BASE_") {
			t.Fatalf("env still has %s", kv)
		}
	}
}

func TestUnsupportedReason(t *testing.T) {
	for _, c := range []struct {
		goos, goarch string
		err          error
		want         string
	}{
		{"linux", "amd64", nil, ""},
		{"darwin", "arm64", nil, ""},
		{"windows", "amd64", nil, "Windows"},
		{"linux", "386", nil, "linux/386"},
		{"freebsd", "amd64", nil, "freebsd/amd64"},
		{"linux", "arm64", errors.New("boom"), "boom"},
	} {
		if got := unsupportedReason(c.goos, c.goarch, c.err); (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("unsupportedReason(%s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("B", 64)
	sums := fmt.Sprintf("%s  x.zip\n%s *y.tar.gz\nshort  z.tar.gz\n", a, b)
	if got, ok := checksumFor(sums, "x.zip"); !ok || got != a {
		t.Fatalf("x.zip = %q %v", got, ok)
	}
	if got, ok := checksumFor(sums, "y.tar.gz"); !ok || got != strings.ToLower(b) {
		t.Fatalf("y.tar.gz = %q %v", got, ok)
	}
	for _, name := range []string{"z.tar.gz", "missing"} {
		if _, ok := checksumFor(sums, name); ok {
			t.Fatalf("%s 不应该找到", name)
		}
	}
}

// 一键更新按文件名下载 Release 里的包，并从包的根目录取 wb2api：和 go-binaries 工作流的打包命令对一下。
func TestAssetNameMatchesWorkflow(t *testing.T) {
	wf, err := os.ReadFile("../../.github/workflows/go-binaries.yml")
	if err != nil {
		t.Skipf("读不到工作流：%v", err)
	}
	for _, want := range []string{
		`NAME="wb2api-panel-v${V}-${{ matrix.goos }}-${{ matrix.goarch }}.tar.gz"`,
		`tar czf "$NAME" wb2api `,
		`sha256sum *.zip *.tar.gz > checksums.txt`,
	} {
		if !strings.Contains(string(wf), want) {
			t.Errorf("go-binaries.yml 里找不到 %q；改了打包方式要同步改 AssetName / extractBinary", want)
		}
	}
	if got := AssetName("1.2.0", "linux", "arm64"); got != "wb2api-panel-v1.2.0-linux-arm64.tar.gz" {
		t.Fatalf("AssetName = %q", got)
	}
}
