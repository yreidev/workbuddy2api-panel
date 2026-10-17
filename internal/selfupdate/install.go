package selfupdate

// 下载与校验：checksums.txt → 安装包（核对 sha256）→ 解出 wb2api → 试运行 -version → 改名就位。
// 每一步失败都删掉临时文件，数据目录里原来的版本不受影响。

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/changelog"
)

const (
	installTimeout = 10 * time.Minute
	trialTimeout   = 10 * time.Second
	archiveMax     = 100 << 20
	binaryMax      = 200 << 20
	checksumsMax   = 64 << 10
)

// AssetName Release 里某个平台的安装包文件名（和 go-binaries 工作流的打包命名一致）。
func AssetName(version, goos, goarch string) string {
	return fmt.Sprintf("wb2api-panel-v%s-%s-%s.tar.gz", version, goos, goarch)
}

// Start 后台更新到 version：下载、校验、试运行都通过后调 Restart。进度看 Status。
func (u *Updater) Start(version string) error {
	if u.unsupported != "" {
		return errors.New(u.unsupported)
	}
	if !changelog.Valid(version) || changelog.Compare(version, u.version) <= 0 {
		return fmt.Errorf("v%s 不比当前的 v%s 新", version, u.version)
	}
	u.mu.Lock()
	if u.busyLocked() {
		u.mu.Unlock()
		return ErrBusy
	}
	u.phase, u.target, u.done, u.total, u.err = PhaseDownloading, version, 0, 0, ""
	u.mu.Unlock()
	log.Printf("[update] 开始更新到 v%s", version)
	go u.install(version)
	return nil
}

func (u *Updater) install(version string) {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	if err := u.stage(ctx, version); err != nil {
		u.mu.Lock()
		u.phase, u.err = PhaseFailed, err.Error()
		u.mu.Unlock()
		log.Printf("[update] 更新到 v%s 失败：%v", version, err)
		return
	}
	log.Printf("[update] v%s 已下载并校验通过，正在重启", version)
	u.setPhase(PhaseRestarting)
	u.restart()
}

// stage 把 version 装进数据目录并写好 current.json。
func (u *Updater) stage(ctx context.Context, version string) error {
	asset := AssetName(version, runtime.GOOS, runtime.GOARCH)
	base := u.DownloadBase
	if base == "" {
		base = DefaultDownloadBase
	}
	base += version + "/"

	var sums strings.Builder
	if err := u.fetch(ctx, version, base+"checksums.txt", &sums, checksumsMax, false); err != nil {
		return err
	}
	want, ok := checksumFor(sums.String(), asset)
	if !ok {
		return fmt.Errorf("checksums.txt 里没有 %s", asset)
	}

	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return fmt.Errorf("创建 %s 失败：%w", u.dir, err)
	}
	arc, err := os.CreateTemp(u.dir, ".download-*")
	if err != nil {
		return fmt.Errorf("数据目录不可写：%w", err)
	}
	defer os.Remove(arc.Name())
	defer arc.Close()
	h := sha256.New()
	if err := u.fetch(ctx, version, base+asset, io.MultiWriter(arc, h), archiveMax, true); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s 的 sha256 和 checksums.txt 对不上（下载不完整或被篡改）", asset)
	}

	u.setPhase(PhaseVerifying)
	if _, err := arc.Seek(0, io.SeekStart); err != nil {
		return err
	}
	tmp, sum, err := extractBinary(arc, u.dir)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // 改名成功后这里是空操作
	if err := trialRun(ctx, tmp, version, u.dir); err != nil {
		return err
	}
	if err := os.Rename(tmp, u.binPath()); err != nil {
		return err
	}
	return writeManifest(u.dir, Manifest{Version: version, SHA256: sum, InstalledAt: time.Now()})
}

// fetch GET url 写进 w，最多 limit 字节；progress 时把进度记进状态。
func (u *Updater) fetch(ctx context.Context, version, url string, w io.Writer, limit int64, progress bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("Release v%s 里还没有 %s，CI 可能还在构建，过几分钟再试", version, path.Base(url))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("下载 %s 失败：HTTP %d", path.Base(url), resp.StatusCode)
	case resp.ContentLength > limit:
		return fmt.Errorf("%s 超过 %d MiB", path.Base(url), limit>>20)
	}
	if progress && resp.ContentLength > 0 {
		u.mu.Lock()
		u.total = resp.ContentLength
		u.mu.Unlock()
	}
	var dst io.Writer = w
	if progress {
		dst = progressWriter{w: w, u: u}
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	if n > limit {
		return fmt.Errorf("%s 超过 %d MiB", path.Base(url), limit>>20)
	}
	return nil
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("拒绝跳转到非 https 地址 %s", req.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("跳转次数过多")
		}
		return nil
	}}
}

type progressWriter struct {
	w io.Writer
	u *Updater
}

func (p progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.u.mu.Lock()
	p.u.done += int64(n)
	p.u.mu.Unlock()
	return n, err
}

// checksumFor 从 sha256sum 的输出里找某个文件的校验值。
func checksumFor(sums, name string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == sha256.Size*2 {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// extractBinary 从 tar.gz 里解出根目录的 wb2api，写到 dir 下的临时文件，返回路径和 sha256。
func extractBinary(r io.Reader, dir string) (string, string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", "", fmt.Errorf("解压安装包失败：%w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return "", "", errors.New("安装包里没有 wb2api")
		}
		if err != nil {
			return "", "", fmt.Errorf("解压安装包失败：%w", err)
		}
		if path.Clean(hdr.Name) != binName || hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size > binaryMax {
			return "", "", fmt.Errorf("安装包里的 wb2api 超过 %d MiB", binaryMax>>20)
		}
		f, err := os.CreateTemp(dir, ".wb2api-*.new")
		if err != nil {
			return "", "", err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(tr, binaryMax))
		if err == nil {
			err = f.Chmod(0o755)
		}
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
			return "", "", fmt.Errorf("写出 wb2api 失败：%w", err)
		}
		return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
	}
}

// trialRun 运行一次 `新程序 -version`：确认能在这台机器上跑（架构对、数据目录没挂 noexec），且就是要装的版本。
func trialRun(ctx context.Context, bin, version, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, trialTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-version")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := ""
		if errors.Is(err, fs.ErrPermission) {
			hint = "（数据目录是否挂载了 noexec？）"
		}
		return fmt.Errorf("试运行新程序失败%s：%v %s", hint, err, strings.TrimSpace(string(out)))
	}
	if got := strings.TrimSpace(string(out)); got != version {
		return fmt.Errorf("试运行新程序报告的版本是 %q，不是 v%s", got, version)
	}
	return nil
}
