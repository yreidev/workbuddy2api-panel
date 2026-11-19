// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package selfupdate

// selfupdate_more_test.go 自更新的补充用例：Launch 入口、清单损坏/写入失败、
// MarkStable/Rollback 的失败守卫、默认 client 的重定向限制，以及 install.go 的
// 下载/解包错误分支。绝不执行真实程序：exec 一律经 stubExec。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// suRoundTrip 假 Transport。
type suRoundTrip func(*http.Request) (*http.Response, error)

func (f suRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// runFn 跑一个可能触发 execFn（stubExec(nil) 会 panic errExecStub）的函数。
func runFn(fn func()) {
	defer func() {
		if r := recover(); r != nil && r != errExecStub {
			panic(r)
		}
	}()
	fn()
}

// Launch：没有安装版本时返回 Updater 且不 exec；有更新且校验通过时 exec 该路径；
// 校验不过不 exec。
func TestLaunchEntry(t *testing.T) {
	t.Run("没有安装版本", func(t *testing.T) {
		calls := stubExec(t, nil)
		u := Launch(t.TempDir(), "1.1.0")
		if u == nil || u.override || !u.Status().Supported {
			t.Fatalf("Updater = %+v", u)
		}
		if len(*calls) != 0 {
			t.Fatalf("不应 exec: %+v", *calls)
		}
	})

	t.Run("有更新且校验通过", func(t *testing.T) {
		dataDir := t.TempDir()
		prep := newUpdater(dataDir, "1.1.0")
		installed(t, prep, Manifest{Version: "1.2.0"}, fakeBinary("1.2.0"))
		calls := stubExec(t, nil)
		runFn(func() { Launch(dataDir, "1.1.0") })
		want := filepath.Join(dataDir, "bin", binName)
		if len(*calls) != 1 || (*calls)[0].path != want || (*calls)[0].args[0] != want {
			t.Fatalf("exec 参数不对: %+v，want %s", *calls, want)
		}
	})

	t.Run("有更新但校验不过", func(t *testing.T) {
		dataDir := t.TempDir()
		prep := newUpdater(dataDir, "1.1.0")
		installed(t, prep, Manifest{Version: "1.2.0"}, fakeBinary("1.2.0"))
		if err := os.WriteFile(prep.binPath(), []byte("tampered"), 0o755); err != nil {
			t.Fatal(err)
		}
		calls := stubExec(t, nil)
		runFn(func() { Launch(dataDir, "1.1.0") })
		if len(*calls) != 0 {
			t.Fatalf("校验不过不应 exec: %+v", *calls)
		}
		m, err := readManifest(prep.dir)
		if err != nil || !strings.Contains(m.Failed, "校验值对不上") {
			t.Fatalf("manifest = %+v, %v", m, err)
		}
	})
}

// newUpdater：bin 目录已存在时解析真实路径；目录不存在也能返回绝对路径。
func TestNewUpdaterPaths(t *testing.T) {
	dataDir := t.TempDir()
	u := newUpdater(dataDir, "1.1.0")
	if !filepath.IsAbs(u.dir) || !strings.HasSuffix(u.dir, string(filepath.Separator)+"bin") {
		t.Fatalf("dir = %q", u.dir)
	}
	// 预建 bin 目录走 EvalSymlinks 成功分支。
	if err := os.MkdirAll(filepath.Join(dataDir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	u2 := newUpdater(dataDir, "1.1.0")
	if u2.dir != u.dir {
		t.Fatalf("预建目录后 dir = %q，want %q", u2.dir, u.dir)
	}
}

// 损坏/非法清单：launch 只记日志，不 exec。
func TestLaunchCorruptManifest(t *testing.T) {
	for _, content := range []string{`{broken`, `{"version":"bad-version"}`} {
		u := newTestUpdater(t, "1.1.0")
		if err := os.MkdirAll(u.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(u.dir, manifestName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		calls := stubExec(t, nil)
		runLaunch(u)
		if len(*calls) != 0 {
			t.Fatalf("清单 %q 损坏时不应 exec: %+v", content, *calls)
		}
	}
}

// 第二次切过去（Attempts=1）只累加次数并打日志。
func TestLaunchSecondAttempt(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	installed(t, u, Manifest{Version: "1.2.0", Attempts: 1}, fakeBinary("1.2.0"))
	calls := stubExec(t, nil)
	runLaunch(u)
	if len(*calls) != 1 {
		t.Fatalf("exec calls = %+v", *calls)
	}
	if m, _ := readManifest(u.dir); m.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", m.Attempts)
	}
}

// 自带版本不比安装的新、但删除安装文件失败：保留现场，不 exec。
func TestLaunchRemoveOverrideFailure(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	installed(t, u, Manifest{Version: "1.1.0"}, fakeBinary("1.1.0"))
	if err := os.Remove(u.binPath()); err != nil {
		t.Fatal(err)
	}
	// 用非空目录占住 binPath，让 os.Remove 失败（root 下同样失败）。
	if err := os.MkdirAll(filepath.Join(u.binPath(), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := stubExec(t, nil)
	runLaunch(u)
	if len(*calls) != 0 {
		t.Fatalf("删除失败时不应 exec: %+v", *calls)
	}
	if _, err := os.Stat(filepath.Join(u.binPath(), "sub")); err != nil {
		t.Fatalf("安装目录不应被删除: %v", err)
	}
}

// MarkStable 的守卫：清单缺失/版本不符/已稳定且次数为 0 时都不写。
func TestMarkStableGuards(t *testing.T) {
	t.Run("清单缺失", func(t *testing.T) {
		u := newTestUpdater(t, "1.2.0")
		u.override = true
		u.MarkStable() // 不 panic
	})
	t.Run("版本不符", func(t *testing.T) {
		u := newTestUpdater(t, "1.2.0")
		u.override = true
		installed(t, u, Manifest{Version: "1.3.0", Attempts: 2}, fakeBinary("1.3.0"))
		u.MarkStable()
		if m, _ := readManifest(u.dir); m.Attempts != 2 || m.Stable {
			t.Fatalf("版本不符不应改动: %+v", m)
		}
	})
	t.Run("已稳定且次数清零", func(t *testing.T) {
		u := newTestUpdater(t, "1.2.0")
		u.override = true
		installed(t, u, Manifest{Version: "1.2.0", Stable: true}, fakeBinary("1.2.0"))
		u.MarkStable()
	})
}

// Rollback：不支持的平台报错；跑着安装版但删文件失败时回到 idle。
func TestRollbackEdges(t *testing.T) {
	t.Run("平台不支持", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		u.unsupported = "unsupported platform"
		if _, err := u.Rollback(); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("密钥格式不兼容时保留正在运行的版本", func(t *testing.T) {
		u := newTestUpdater(t, "1.14.0-rc.1")
		u.override, u.baseVersion = true, "1.13.2"
		if err := os.MkdirAll(u.dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(u.binPath(), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		restarting, err := u.Rollback()
		if restarting || !errors.Is(err, ErrKeyDowngrade) || u.Status().Phase != PhaseIdle {
			t.Fatal(restarting, err)
		}
		if _, err := os.Stat(u.binPath()); err != nil {
			t.Fatal("正在运行的版本被移除", err)
		}
	})
	t.Run("删除失败回到 idle", func(t *testing.T) {
		u := newTestUpdater(t, "1.2.0")
		u.override = true
		if err := os.MkdirAll(filepath.Join(u.binPath(), "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		restarting, err := u.Rollback()
		if err == nil || restarting {
			t.Fatalf("Rollback = %v, %v", restarting, err)
		}
		if s := u.Status(); s.Phase != PhaseIdle || s.Target != "" {
			t.Fatalf("失败后应回到 idle: %+v", s)
		}
	})
}

// restart 没接回调：进入 failed 状态并给出可操作提示。
func TestRestartWithoutCallback(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	u.Restart = nil
	u.restart()
	s := u.Status()
	if s.Phase != PhaseFailed || !strings.Contains(s.Error, "没有接入重启") {
		t.Fatalf("status = %+v", s)
	}
}

// 清单读写：缺失/损坏/版本非法/写入目录不存在。
func TestManifestEdges(t *testing.T) {
	dir := t.TempDir()
	if _, err := readManifest(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺失清单 err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(`{broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(dir); err == nil {
		t.Fatal("损坏清单应报错")
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(`{"version":"not-a-version"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(dir); err == nil || !strings.Contains(err.Error(), "版本号") {
		t.Fatalf("非法版本 err = %v", err)
	}
	if err := writeManifest(filepath.Join(dir, "missing"), Manifest{Version: "1.2.0"}); err == nil {
		t.Fatal("目录不存在时 writeManifest 应报错")
	}
}

// fileSHA256：文件缺失与读取失败。
func TestFileSHA256Errors(t *testing.T) {
	if _, err := fileSHA256(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("缺失文件应报错")
	}
	if _, err := fileSHA256(t.TempDir()); err == nil { // 目录：Open 成功、Read 失败
		t.Fatal("目录应报错")
	}
}

// client()：显式 Client 优先；默认 client 拒绝非 https 跳转与过多跳转。
func TestUpdaterClient(t *testing.T) {
	t.Run("显式 Client", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		var used atomic.Bool
		u.Client = &http.Client{Transport: suRoundTrip(func(*http.Request) (*http.Response, error) {
			used.Store(true)
			return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: 2,
				Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})}
		var buf bytes.Buffer
		if err := u.fetch(context.Background(), "1.2.0", "http://example.invalid/checksums.txt", &buf, 100, false); err != nil {
			t.Fatal(err)
		}
		if !used.Load() || buf.String() != "ok" {
			t.Fatalf("显式 Client 未生效: used=%v body=%q", used.Load(), buf.String())
		}
	})

	// 默认 client 用 http.DefaultTransport 发起请求；把它的 Transport 换成测试
	// TLS 服务器信任的 Transport，才能在本机走完 https → 重定向检查。
	withTLSTransport := func(t *testing.T, srv *httptest.Server) {
		t.Helper()
		old := http.DefaultTransport
		http.DefaultTransport = srv.Client().Transport
		t.Cleanup(func() { http.DefaultTransport = old })
	}

	t.Run("拒绝非 https 跳转", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.invalid/insecure", http.StatusFound)
		}))
		defer srv.Close()
		withTLSTransport(t, srv)
		u := newTestUpdater(t, "1.1.0")
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, err := u.client().Do(req)
		if err == nil || !strings.Contains(err.Error(), "拒绝跳转到非 https") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("跳转次数过多", func(t *testing.T) {
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, srv.URL+r.URL.Path, http.StatusFound)
		}))
		defer srv.Close()
		withTLSTransport(t, srv)
		u := newTestUpdater(t, "1.1.0")
		req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
		_, err := u.client().Do(req)
		if err == nil || !strings.Contains(err.Error(), "跳转次数过多") {
			t.Fatalf("err = %v", err)
		}
	})
}

// stage：数据目录不可建、目标二进制被目录占用时都返回错误且不留下半成品。
func TestStageErrors(t *testing.T) {
	t.Run("数据目录不可建", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		srv := serveRelease(t, release{version: "1.2.0", archive: tarGz(t, "wb2api", fakeBinary("1.2.0"))})
		u.DownloadBase = srv.URL + "/"
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		u.dir = blocker
		err := u.stage(context.Background(), "1.2.0")
		if err == nil || !strings.Contains(err.Error(), "创建") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("目标二进制被目录占用", func(t *testing.T) {
		u := newTestUpdater(t, "1.1.0")
		srv := serveRelease(t, release{version: "1.2.0", archive: tarGz(t, "wb2api", fakeBinary("1.2.0"))})
		u.DownloadBase = srv.URL + "/"
		if err := os.MkdirAll(filepath.Join(u.binPath(), "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		u.Restart = func() { t.Error("失败时不应重启") }
		if err := u.Start("1.2.0"); err != nil {
			t.Fatal(err)
		}
		s := waitPhase(t, u)
		if s.Phase != PhaseFailed {
			t.Fatalf("status = %+v", s)
		}
	})
}

// fetch：非法 URL、连接失败、非 200、超过上限、短读、chunked 超限。
func TestFetchErrors(t *testing.T) {
	u := newTestUpdater(t, "1.1.0")
	ctx := context.Background()

	if err := u.fetch(ctx, "1.2.0", "http://exa\x00mple/x", io.Discard, 10, false); err == nil {
		t.Fatal("非法 URL 应报错")
	}
	if err := u.fetch(ctx, "1.2.0", "http://localhost:1/x", io.Discard, 10, false); err == nil || !strings.Contains(err.Error(), "下载") {
		t.Fatalf("连接失败 err = %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/500":
			w.WriteHeader(http.StatusInternalServerError)
		case "/big-header":
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("0123456789"))
		case "/short":
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("short"))
		case "/chunked":
			if f, ok := w.(http.Flusher); ok {
				f.Flush() // 先发头 → Transfer-Encoding: chunked（ContentLength=-1）
			}
			_, _ = w.Write([]byte("0123456789"))
		}
	}))
	defer srv.Close()

	if err := u.fetch(ctx, "1.2.0", srv.URL+"/500", io.Discard, 10, false); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("500 err = %v", err)
	}
	if err := u.fetch(ctx, "1.2.0", srv.URL+"/big-header", io.Discard, 2, false); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("Content-Length 超限 err = %v", err)
	}
	if err := u.fetch(ctx, "1.2.0", srv.URL+"/short", io.Discard, 1000, false); err == nil || !strings.Contains(err.Error(), "下载") {
		t.Fatalf("短读 err = %v", err)
	}
	if err := u.fetch(ctx, "1.2.0", srv.URL+"/chunked", io.Discard, 2, false); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("chunked 超限 err = %v", err)
	}
}

// extractBinary：gzip/tar 损坏、目录不可写、包内文件超大。
func TestExtractBinaryErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := extractBinary(strings.NewReader("not gzip"), dir); err == nil {
		t.Fatal("非 gzip 应报错")
	}

	var badTar bytes.Buffer
	gz := gzip.NewWriter(&badTar)
	_, _ = gz.Write([]byte("garbage"))
	_ = gz.Close()
	if _, _, err := extractBinary(bytes.NewReader(badTar.Bytes()), dir); err == nil {
		t.Fatal("损坏 tar 应报错")
	}

	valid := tarGz(t, "wb2api", fakeBinary("1.2.0"))
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := extractBinary(bytes.NewReader(valid), blocker); err == nil {
		t.Fatal("目标目录不可写应报错")
	}

	// 包内文件声明超过上限：只需 tar 头，无需真实数据。
	var huge bytes.Buffer
	gz2 := gzip.NewWriter(&huge)
	tw := tar.NewWriter(gz2)
	if err := tw.WriteHeader(&tar.Header{Name: "wb2api", Mode: 0o755, Size: binaryMax + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_ = gz2.Close() // 不关 tar writer：只需头部可解析
	if _, _, err := extractBinary(bytes.NewReader(huge.Bytes()), t.TempDir()); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("超大文件 err = %v", err)
	}
}

// trialRun：文件不可执行时带上 noexec 提示。
func TestTrialRunPermissionHint(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "wb2api")
	if err := os.WriteFile(bin, fakeBinary("1.2.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := trialRun(context.Background(), bin, "1.2.0", dir)
	if err == nil || !strings.Contains(err.Error(), "noexec") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, os.ErrPermission) && !strings.Contains(err.Error(), "permission denied") {
		t.Logf("平台错误文案: %v", err)
	}
}
