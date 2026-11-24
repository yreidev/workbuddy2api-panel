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
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
)

const qoderUMIDPackage = "https://registry.npmjs.org/@qodercn-ai/qoderclicn/-/qoderclicn-1.1.96.tgz"

func prepareQoderHelper(ctx context.Context, dir, proxyURL string) (string, error) {
	if goruntime.GOOS != "linux" {
		return "", errors.New("Qoder 自动 UMID 当前仅支持 Linux；其他系统须导入官方设备身份")
	}
	digest := ""
	switch goruntime.GOARCH {
	case "amd64":
		digest = "e30b307eaae12680d2f4e220d4d34b0ea4f70355a624d6d55ee916e03f7d7478"
	case "arm64":
		digest = "2df9d2c5aa44cb6c45ad27f493bf3dca2aaf1e067fa0ad6e88c6cad0153329fe"
	default:
		return "", fmt.Errorf("Qoder 官方组件不支持 Linux %s", goruntime.GOARCH)
	}
	path := filepath.Join(dir, ".bin", "runtime-info-linux-"+qoderMachineArch()+"-"+digest[:16])
	if raw, err := os.ReadFile(path); err == nil {
		if fmt.Sprintf("%x", sha256.Sum256(raw)) == digest {
			if err := os.Chmod(path, 0500); err != nil {
				return "", fmt.Errorf("设置 UMID 组件权限: %w", err)
			}
			return path, nil
		}
		log.Printf("[providers] Qoder UMID 组件 SHA-256 不符，重新从官方包提取")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取 UMID 组件: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, qoderUMIDPackage, nil)
	if err != nil {
		return "", err
	}
	client := http.DefaultClient
	if proxyURL != "" {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialQoderUMIDProxy(ctx, proxyURL, network, address)
		}
		defer transport.CloseIdleConnections()
		client = &http.Client{Transport: transport}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载官方 Qoder 1.1.96 组件包: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载官方 Qoder 1.1.96 组件包: HTTP %d", resp.StatusCode)
	}
	binary, err := extractQoderHelper(io.LimitReader(resp.Body, 96<<20), digest)
	if err != nil {
		return "", err
	}
	if err := writeProviderFile(path, binary, 0500); err != nil {
		return "", err
	}
	return path, nil
}

func extractQoderHelper(reader io.Reader, digest string) ([]byte, error) {
	z, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("读取 Qoder 官方压缩包: %w", err)
	}
	defer z.Close()
	tr := tar.NewReader(io.LimitReader(z, 128<<20))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("Qoder 官方包缺少预期的 CLI bundle")
		}
		if err != nil {
			return nil, fmt.Errorf("读取 Qoder 官方包目录: %w", err)
		}
		if header.Name != "package/bundle/qoderclicn.js" || header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > 64<<20 {
			return nil, errors.New("Qoder 官方 bundle 超过 64 MiB 上限")
		}
		bundle, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("读取 Qoder 官方 bundle: %w", err)
		}
		// 不执行 CLI 或 npm 安装脚本，仅取 ELF 常量；不依赖压缩后的变量名。
		for {
			start := bytes.Index(bundle, []byte(`="f0VMRg`))
			if start < 0 {
				return nil, errors.New("Qoder 官方组件 SHA-256 与固定版本不符")
			}
			bundle = bundle[start+2:]
			end := bytes.IndexByte(bundle, '"')
			if end < 0 {
				return nil, errors.New("Qoder 官方组件 base64 常量不完整")
			}
			decoded, decodeErr := base64.StdEncoding.DecodeString(string(bundle[:end]))
			if decodeErr == nil && fmt.Sprintf("%x", sha256.Sum256(decoded)) == digest {
				return decoded, nil
			}
			bundle = bundle[end+1:]
		}
	}
}

type qoderBoundedOutput struct {
	bytes.Buffer
	limit int
}

func (w *qoderBoundedOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.limit {
		return 0, errors.New("UMID 组件输出超过上限")
	}
	return w.Buffer.Write(p)
}

// runQoderMachine 只传官方 Linux 的 environment 参数，不传账号或授权令牌。
func runQoderMachine(ctx context.Context, binary string, environment int) (qoderMachineInfo, error) {
	return withQoderSDKLocale(binary, environment, func() (qoderMachineInfo, error) {
		return runQoderMachineCommand(ctx, binary, binary, []string{strconv.Itoa(environment)})
	})
}

// withQoderSDKLocale 按地区（environment 0/4）隔离官方 SDK 的 .locale_cfg 状态文件。
// 官方 helper 固定读写 <pw_dir>/.config/.locale_cfg（容器与一键安装脚本中均软链至 data/qoder-umid/sdk），
// 且不区分国内/国际环境；共享同一份文件会导致后初始化的另一地区直接命中前者的本地缓存、
// 跳过官方网络交互（代理模式下触发「离线回退」失败，直连模式下串用另一地区身份）。
func withQoderSDKLocale(binary string, environment int, fn func() (qoderMachineInfo, error)) (qoderMachineInfo, error) {
	umidDir := filepath.Dir(filepath.Dir(binary))
	sdkDir := filepath.Join(umidDir, "sdk")
	if err := os.MkdirAll(sdkDir, 0700); err != nil {
		return qoderMachineInfo{}, fmt.Errorf("创建 UMID SDK 状态目录: %w", err)
	}
	activeFile := filepath.Join(sdkDir, ".locale_cfg")
	envFile := filepath.Join(sdkDir, fmt.Sprintf(".locale_cfg.env-%d", environment))
	preserveUnownedOnFail := false

	if _, err := os.Stat(envFile); errors.Is(err, os.ErrNotExist) {
		if activeRaw, rErr := os.ReadFile(activeFile); rErr == nil {
			existingSlots, _ := filepath.Glob(filepath.Join(sdkDir, ".locale_cfg.env-*"))
			if len(existingSlots) == 0 {
				_, err0 := os.Stat(filepath.Join(umidDir, "environment-0.json"))
				_, err4 := os.Stat(filepath.Join(umidDir, "environment-4.json"))
				has0, has4 := err0 == nil, err4 == nil
				owner := -1
				switch {
				case has0 && !has4:
					owner = 0
				case has4 && !has0:
					owner = 4
				case has0 && has4:
					// 旧版双区共享同一份 .locale_cfg：归属国内 0 保留既有设备身份，国际 4 独立初始化。
					owner = 0
				}
				if owner >= 0 {
					ownerFile := filepath.Join(sdkDir, fmt.Sprintf(".locale_cfg.env-%d", owner))
					if wErr := writeProviderFile(ownerFile, activeRaw, 0600); wErr != nil {
						return qoderMachineInfo{}, fmt.Errorf("迁移旧版 UMID SDK 状态: %w", wErr)
					}
				} else {
					preserveUnownedOnFail = true
				}
			}
		} else if !errors.Is(rErr, os.ErrNotExist) {
			return qoderMachineInfo{}, fmt.Errorf("读取 UMID SDK 状态: %w", rErr)
		}
	}

	if envRaw, err := os.ReadFile(envFile); err == nil {
		if wErr := writeProviderFile(activeFile, envRaw, 0600); wErr != nil {
			return qoderMachineInfo{}, fmt.Errorf("准备 UMID SDK 状态: %w", wErr)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if !preserveUnownedOnFail {
			if rmErr := os.Remove(activeFile); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				return qoderMachineInfo{}, fmt.Errorf("清理残留 UMID SDK 状态: %w", rmErr)
			}
		}
	} else {
		return qoderMachineInfo{}, fmt.Errorf("读取环境 UMID SDK 状态: %w", err)
	}

	committed := false
	defer func() {
		if !committed && preserveUnownedOnFail {
			return
		}
		_ = os.Remove(activeFile)
	}()

	info, err := fn()
	if err != nil {
		return info, err
	}
	if activeRaw, rErr := os.ReadFile(activeFile); rErr == nil {
		if wErr := writeProviderFile(envFile, activeRaw, 0600); wErr != nil {
			return qoderMachineInfo{}, fmt.Errorf("保存环境 UMID SDK 状态: %w", wErr)
		}
		committed = true
	} else if !errors.Is(rErr, os.ErrNotExist) {
		return qoderMachineInfo{}, fmt.Errorf("读取更新后 UMID SDK 状态: %w", rErr)
	}
	return info, nil
}

func runQoderMachineCommand(ctx context.Context, binary, command string, args []string) (qoderMachineInfo, error) {
	if err := ctx.Err(); err != nil {
		return qoderMachineInfo{}, err
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(filepath.Dir(binary)), "sdk"), 0700); err != nil {
		return qoderMachineInfo{}, fmt.Errorf("创建 UMID SDK 状态目录: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.Command(command, args...)
	stdout := &qoderBoundedOutput{limit: 1 << 20}
	stderr := &qoderBoundedOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return qoderMachineInfo{}, fmt.Errorf("启动官方 UMID 组件（Linux 需 glibc 或 gcompat/libstdc++）: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	children := map[int]string{}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var err error
wait:
	for {
		collectQoderChildren(cmd.Process.Pid, children)
		select {
		case err = <-done:
			break wait
		case <-ctx.Done():
			collectQoderChildren(cmd.Process.Pid, children)
			// 只终止本次启动的进程及核验过启动时间的子进程，不按名字或进程组杀进程。
			killErr := cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
			killErr = errors.Join(killErr, killQoderChildren(children))
			<-done
			return qoderMachineInfo{}, fmt.Errorf("官方 UMID 组件执行超时或取消: %w", errors.Join(ctx.Err(), killErr))
		case <-tick.C:
		}
	}
	if killErr := killQoderChildren(children); killErr != nil {
		return qoderMachineInfo{}, killErr
	}
	if err != nil {
		// stderr 可能包含设备资料，不直接写入日志或 API 错误。
		return qoderMachineInfo{}, fmt.Errorf("官方 UMID 组件执行失败（stderr %d 字节）: %w", stderr.Len(), err)
	}
	line, _, _ := bytes.Cut(stdout.Bytes(), []byte{'\n'})
	var info qoderMachineInfo
	if err := json.Unmarshal(line, &info); err != nil || !validQoderMachineInfo(info) {
		return qoderMachineInfo{}, errors.New("官方 UMID 组件首行不是有效的设备 JSON")
	}
	return info, nil
}

func qoderProcessStart(pid int) string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	return qoderLiveProcessStart(raw)
}

func qoderLiveProcessStart(raw []byte) string {
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	switch fields[0] {
	case "Z", "X", "x":
		// 进程已退出，回收前 /proc 仍可能短暂保留记录。
		return ""
	}
	return fields[19]
}

func collectQoderChildren(pid int, children map[int]string) {
	tasks, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
	if err != nil {
		return
	}
	for _, task := range tasks {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%s/children", pid, task.Name()))
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(raw)) {
			child, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			if _, seen := children[child]; seen {
				continue
			}
			if start := qoderProcessStart(child); start != "" {
				children[child] = start
				collectQoderChildren(child, children)
			}
		}
	}
}

func killQoderChildren(children map[int]string) error {
	var result error
	for pid, start := range children {
		if qoderProcessStart(pid) == start {
			if process, err := os.FindProcess(pid); err == nil {
				if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
					result = errors.Join(result, fmt.Errorf("回收 UMID 子进程 PID=%d: %w", pid, err))
				}
			} else {
				result = errors.Join(result, fmt.Errorf("定位 UMID 子进程 PID=%d: %w", pid, err))
			}
		}
	}
	return result
}
