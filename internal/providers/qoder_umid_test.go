// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testQoderMachine(t *testing.T) (*Manager, *qoderMachine, *atomic.Int32) {
	t.Helper()
	m, _ := testManager(t, filepath.Join(t.TempDir(), "providers.json"))
	if err := m.EnableQoderUMID(""); err != nil {
		t.Fatal(err)
	}
	u := m.qoderMachine
	calls := &atomic.Int32{}
	u.prepare = func(context.Context, string) (string, error) { return "fixture-helper", nil }
	u.run = func(_ context.Context, _ string, env int, _ string) (qoderMachineInfo, error) {
		return qoderMachineInfo{Token: fmt.Sprintf("fixture-token-%d-%d", env, calls.Add(1)), Type: "fixture-type", Code: "fixture-code"}, nil
	}
	return m, u, calls
}

func TestQoderUMIDSingleFlightRegionsAndRestart(t *testing.T) {
	_, u, calls := testQoderMachine(t)
	now := time.Now()
	u.now = func() time.Time { return now }
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if info, err := u.get(t.Context(), "qoder:cn", ""); err != nil || info.Token != "fixture-token-0-1" {
				t.Errorf("并发初始化没有合并：%v", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("首次初始化重复运行组件")
	}
	global, err := u.get(t.Context(), "qoder:global", "")
	if err != nil || global.Token != "fixture-token-4-2" {
		t.Fatal("国内和国际没有独立初始化", err)
	}
	for _, env := range []int{0, 4} {
		path := filepath.Join(u.dir, fmt.Sprintf("environment-%d.json", env))
		stat, err := os.Stat(path)
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatal("UMID 缓存权限错误", err)
		}
		var cache qoderMachineCache
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &cache); err != nil || cache.Environment != env || cache.RefreshAt.Sub(cache.UpdatedAt) < 55*time.Minute || cache.RefreshAt.Sub(cache.UpdatedAt) > 65*time.Minute {
			t.Fatal("缓存地区或刷新周期错误", err)
		}
	}
	m, _, _ := testQoderMachine(t)
	m.qoderMachine.dir = u.dir
	m.qoderMachine.run = func(context.Context, string, int, string) (qoderMachineInfo, error) {
		t.Error("重启后没有恢复仍有效的缓存")
		return qoderMachineInfo{}, errors.New("不应执行")
	}
	if info, err := m.qoderMachine.get(t.Context(), "qoder:global", "http://fixture.invalid:8080"); err != nil || info != global {
		t.Fatal("重启后身份不一致", err)
	}
	now = now.Add(66 * time.Minute)
	if info, err := u.get(t.Context(), "qoder:cn", ""); err != nil || info.Token != "fixture-token-0-3" {
		t.Fatal("到期没有刷新", err)
	}
}

func TestQoderUMIDFailuresKeepDurableCache(t *testing.T) {
	_, u, _ := testQoderMachine(t)
	now := time.Now()
	u.now = func() time.Time { return now }
	old, err := u.get(t.Context(), "qoder:cn", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(66 * time.Minute)
	u.run = func(context.Context, string, int, string) (qoderMachineInfo, error) {
		return qoderMachineInfo{}, errors.New("fixture component failed")
	}
	if info, err := u.get(t.Context(), "qoder:cn", ""); err != nil || info != old {
		t.Fatal("刷新失败丢失旧身份", err)
	}
	u.run = func(context.Context, string, int, string) (qoderMachineInfo, error) {
		return qoderMachineInfo{Token: "new-fixture", Type: "type", Code: "code"}, nil
	}
	now = now.Add(2 * time.Minute)
	path := filepath.Join(u.dir, "environment-0.json")
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if info, err := u.get(t.Context(), "qoder:cn", ""); err != nil || info != old || u.cache[0].Info != old {
		t.Fatal("写盘失败后采用了未保存的身份", err)
	}
	_, empty, calls := testQoderMachine(t)
	empty.run = func(_ context.Context, _ string, _ int, proxyURL string) (qoderMachineInfo, error) {
		if proxyURL != "http://fixture.invalid:8080" {
			t.Error("未传入所选代理")
		}
		return qoderMachineInfo{}, errors.New("fixture proxy failed")
	}
	if _, err := empty.get(t.Context(), "qoder:cn", "http://fixture.invalid:8080"); err == nil || calls.Load() != 0 {
		t.Fatal("代理失败后没有保留错误")
	}
	if _, err := u.get(t.Context(), "qoder:global", ""); err != nil {
		t.Fatal("国内失败影响了独立国际环境", err)
	}
	_, invalid, _ := testQoderMachine(t)
	if err := os.MkdirAll(invalid.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid.dir, "environment-0.json"), []byte(`{"version":1,"environment":4}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := invalid.get(t.Context(), "qoder:cn", ""); err == nil {
		t.Fatal("忽略了错误地区的缓存")
	}
}

func TestQoderUMIDManagerPreservesCredentials(t *testing.T) {
	m, u, calls := testQoderMachine(t)
	credential := Credential{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", UID: "fixture-uid", UserType: "free", MachineID: "fixture-machine", DeviceID: "fixture-device", DeviceKey: "", ClientID: "fixture-client", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	id, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Credential: credential})
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.credential(t.Context(), id, "qoder:cn")
	if err != nil || !c.MachineLocal || c.MachineToken == "" || c.MachineCode == "" || c.MachineHostname == "" {
		t.Fatal("没有保存本机初始化的设备身份", err)
	}
	if c.AccessToken != credential.AccessToken || c.RefreshToken != credential.RefreshToken || c.MachineID != credential.MachineID || c.DeviceID != credential.DeviceID || c.ClientID != credential.ClientID {
		t.Fatal("设备初始化改写了账号令牌或原设备 ID")
	}
	// 到期的账号令牌和 UMID 在同一账号锁内更新，不允许旧快照覆盖刷新令牌。
	m.data.Accounts[0].Credential.ExpiresAt = 1
	u.cache = map[int]qoderMachineCache{}
	if err := os.Remove(filepath.Join(u.dir, "environment-0.json")); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.credential(t.Context(), id, "qoder:cn")
	if err != nil || fresh.RefreshToken != "rotated-refresh" || fresh.MachineToken == c.MachineToken || calls.Load() != 2 {
		t.Fatal("设备更新与令牌刷新互相覆盖", err)
	}
	exported, err := m.Export([]string{id})
	if err != nil || len(exported) != 1 || exported[0].Credential.MachineLocal || exported[0].Credential.MachineToken != fresh.MachineToken {
		t.Fatal("导出没有保留可迁移身份", err)
	}
	view, _ := json.Marshal(m.List())
	if strings.Contains(string(view), fresh.MachineToken) || strings.Contains(string(view), "machine_code") {
		t.Fatal("普通账号列表泄露设备身份")
	}
	reopened, err := Open(m.path, nil)
	if err != nil || !reflect.DeepEqual(reopened.data.Accounts[0].Credential, fresh) {
		t.Fatal("重启丢失设备或最新刷新令牌", err)
	}
	// 仅更新授权时沿用原 MachineID / DeviceID / 本机 UMID。
	credential.AccessToken, credential.RefreshToken = fresh.AccessToken, fresh.RefreshToken
	credential.MachineID, credential.DeviceID = "", ""
	if _, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Credential: credential}); err != nil {
		t.Fatal(err)
	}
	got := m.data.Accounts[0].Credential
	if got.MachineID != fresh.MachineID || got.DeviceID != fresh.DeviceID || got.MachineToken != fresh.MachineToken || !got.MachineLocal {
		t.Fatal("重新导入授权更换了已有设备")
	}
	// 迁移来的官方身份不被本机生命周期替换。
	imported := fresh
	imported.MachineToken, imported.MachineCode, imported.MachineLocal = "imported-fixture", "imported-code", false
	if _, err := m.Import(t.Context(), AccountInput{Target: "qoder:cn", Credential: imported}); err != nil {
		t.Fatal(err)
	}
	if got, err := m.credential(t.Context(), id, "qoder:cn"); err != nil || got != imported || calls.Load() != 2 {
		t.Fatal("覆盖了导入的设备身份", err)
	}
}

func TestQoderUMIDCancelledWait(t *testing.T) {
	_, u, _ := testQoderMachine(t)
	u.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := u.get(ctx, "qoder:cn", ""); !errors.Is(err, context.Canceled) {
		t.Fatal("等待组件锁时没有响应取消", err)
	}
}

func TestQoderUMIDFailureDoesNotConsumeRefreshToken(t *testing.T) {
	m, u, calls := testQoderMachine(t)
	m.qoderMachine = nil
	id := importFake(t, m, "qoder:cn", "fixture-user")
	m.qoderMachine = u
	m.data.Accounts[0].Credential.ExpiresAt = 1
	adapter := m.adapters["qoder"].(*fakeAdapter)
	now := time.Now()
	u.now = func() time.Time { return now }
	run := u.run
	u.run = func(context.Context, string, int, string) (qoderMachineInfo, error) {
		return qoderMachineInfo{}, errors.New("fixture startup failure")
	}
	if _, err := m.credential(t.Context(), id, "qoder:cn"); err == nil {
		t.Fatal("设备初始化失败却继续刷新账号令牌")
	}
	if adapter.refreshed.Load() != 0 || m.running[id].blocked || m.data.Accounts[0].Credential.RefreshToken != "refresh" {
		t.Fatal("设备失败消耗或封锁了原刷新令牌")
	}
	if _, err := os.Stat(m.guardPath(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("设备失败错误地产生一次性刷新标记", err)
	}
	now = now.Add(2 * time.Minute)
	u.run = run
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			c, err := m.credential(t.Context(), id, "qoder:cn")
			if err != nil || c.RefreshToken != "rotated-refresh" || !c.MachineLocal || c.MachineToken != "fixture-token-0-1" {
				t.Errorf("并发取得凭证丢失设备或新令牌：%v", err)
			}
		})
	}
	wg.Wait()
	if adapter.refreshed.Load() != 1 || calls.Load() != 1 {
		t.Fatal("并发重复消费刷新令牌或初始化设备")
	}
}

func TestQoderUMIDExtractFixedArtifact(t *testing.T) {
	binary := append([]byte{0x7f, 'E', 'L', 'F', 2, 1}, []byte("fixture-only-binary")...)
	digest := fmt.Sprintf("%x", sha256.Sum256(binary))
	for _, tc := range []struct{ name, bundle, want string }{
		{"valid", `var unrelated="f0VMRg==",helper="` + base64.StdEncoding.EncodeToString(binary) + `"`, digest},
		{"tampered", `var helper="` + base64.StdEncoding.EncodeToString(binary) + `"`, strings.Repeat("0", 64)},
		{"missing", `var helper="unconfirmed"`, digest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			z := gzip.NewWriter(&archive)
			tr := tar.NewWriter(z)
			if err := tr.WriteHeader(&tar.Header{Name: "package/bundle/qoderclicn.js", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(tc.bundle))}); err != nil {
				t.Fatal(err)
			}
			_, _ = tr.Write([]byte(tc.bundle))
			_ = tr.Close()
			_ = z.Close()
			got, err := extractQoderHelper(bytes.NewReader(archive.Bytes()), tc.want)
			if tc.name == "valid" {
				if err != nil || !bytes.Equal(got, binary) {
					t.Fatal("未提取正确的固定 SHA-256 组件", err)
				}
			} else if err == nil {
				t.Fatal("接受了未核实的组件")
			}
		})
	}
}

func TestQoderUMIDProcessOutputAndTimeout(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("官方组件与子进程回收当前只支持 Linux")
	}
	for _, tc := range []struct {
		name, script string
		valid        bool
	}{
		{"valid", `printf '%s\n' '{"machineToken":"fixture-token","machineType":"type","machineCode":"code"}' 'diagnostic'`, true},
		{"empty", `printf '%s\n' '{"machineToken":"","machineType":"type","machineCode":"code"}'`, false},
		{"invalid", `printf '%s\n' 'private-fixture-not-json' >&2; exit 1`, false},
		{"overflow", `head -c 1048577 /dev/zero`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".bin", "fixture-helper")
			if err := writeProviderFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0500); err != nil {
				t.Fatal(err)
			}
			_, err := runQoderMachine(t.Context(), path, 0)
			if (err == nil) != tc.valid || err != nil && strings.Contains(err.Error(), "private-fixture") {
				t.Fatal("组件输出验证或错误脱敏错误", err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), ".bin", "fixture-helper")
	childFile := filepath.Join(filepath.Dir(path), "child.pid")
	t.Setenv("WB2A_UMID_CHILD_FIXTURE", childFile)
	script := "#!/bin/sh\nsleep 30 &\nprintf '%s' \"$!\" >\"$WB2A_UMID_CHILD_FIXTURE\"\nwait\n"
	if err := writeProviderFile(path, []byte(script), 0500); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := runQoderMachine(ctx, path, 4); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("超时没有退出", err)
	}
	raw, err := os.ReadFile(childFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for qoderProcessStart(pid) != "" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if qoderProcessStart(pid) != "" {
		t.Fatal("超时留下本次启动的子进程")
	}
}

func TestQoderLiveProcessStart(t *testing.T) {
	for _, state := range []string{"R", "S", "Z", "X", "x"} {
		stat := "100 (fixture (sleep)) " + state + strings.Repeat(" 0", 18) + " 42 0"
		want := ""
		if state == "R" || state == "S" {
			want = "42"
		}
		if got := qoderLiveProcessStart([]byte(stat)); got != want {
			t.Errorf("状态 %s 的启动标记为 %q，预期 %q", state, got, want)
		}
	}
}

func TestQoderHostnameNormalization(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{" fixture-host ", "fixture-host"},
		{"设备.example", "xn--srs139i.example"},
		{"a\n\nb", "a-b-" + fmt.Sprintf("%x", sha256.Sum256([]byte("a\n\nb")))[:8]},
		{"", ""},
	} {
		if got := qoderHostname(tc.in); got != tc.want {
			t.Errorf("hostname 规范化错误：%q != %q", got, tc.want)
		}
	}
	if host := qoderHostname(strings.Repeat("x", 120)); len(host) != 96 {
		t.Fatal("hostname 未限制为 96 字节")
	}
}
