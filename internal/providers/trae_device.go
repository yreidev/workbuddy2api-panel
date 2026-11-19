// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package providers

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// 设备辅助实现改写自 cli2api（MIT，Copyright (c) 2026 caigee-cmd），许可见本目录 NOTICE。
// 设备信息与签名文本按 cli2api 5224c100 的协议记录实现，来源见 docs/official-client.md。
func traeDevice(c Credential) (map[string]any, error) {
	// 官方对取不到的硬件字段发空字符串，Linux OSVersion 读取失败时回落 Linux。
	// 不采集网关宿主的姓名、主机名和硬件信息，持久化身份与私钥保持原值。
	info := map[string]any{"DeviceID": c.DeviceID, "MachineID": c.MachineID, "PlatformCode": "IDE_PC", "DeviceType": "PC", "DeviceName": "", "DeviceModel": "", "ClientVersion": traeVersion, "DeviceBrand": "", "DeviceCPU": "", "OSInfo": "linux", "OSVersion": "Linux"}
	if c.DeviceKey != "" {
		key, err := traePrivateKey(c.DeviceKey)
		if err != nil {
			return nil, err
		}
		raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			return nil, err
		}
		info["DevicePublicKey"] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}))
	}
	return info, nil
}
func newTraeKey() (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})), nil
}
func traePrivateKey(raw string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("Trae 设备密钥不是 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("Trae 设备密钥不是 PKCS8 私钥")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("Trae 设备密钥必须是 P-256 私钥")
	}
	return key, nil
}
func traeProof(c Credential, path, nonce string, timestamp int64) (string, error) {
	key, err := traePrivateKey(c.DeviceKey)
	if err != nil {
		return "", err
	}
	payload := strings.Join([]string{"POST", path, c.ClientID, c.RefreshToken, fmt.Sprint(timestamp), nonce}, "\n")
	digest := sha256.Sum256([]byte(payload))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}
