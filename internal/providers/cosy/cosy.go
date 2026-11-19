// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// COSY 编解码与签名改写自 cli2api qauth（MIT），其源自 EchoPing 的 Qoder-2API-Go。
// Copyright (c) 2026 EchoPing；完整上游署名与许可见 本目录 NOTICE。
package cosy

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cosySignatureHash "crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const CosyVersion = "1.1.89"
const UserAgent = "Bun/1.4.2"
const customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
const stdAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

var s2c [128]byte
var c2s [128]byte
var s2cOK [128]bool
var c2sOK [128]bool

func init() {
	for i := 0; i < 64; i++ {
		s := stdAlphabet[i]
		c := customAlphabet[i]
		s2c[s] = c
		s2cOK[s] = true
		c2s[c] = s
		c2sOK[c] = true
	}
	s2c['='] = '$'
	s2cOK['='] = true
	c2s['$'] = '='
	c2sOK['$'] = true
}

func Encode(plaintext []byte) string {
	std := base64.StdEncoding.EncodeToString(plaintext)
	n := len(std)
	a := n / 3
	rearranged := make([]byte, n)
	copy(rearranged[:a], std[n-a:])
	copy(rearranged[a:n-a], std[a:n-a])
	copy(rearranged[n-a:], std[:a])
	result := make([]byte, n)
	for i, ch := range rearranged {
		if ch >= 128 || !s2cOK[ch] {
			panic(fmt.Sprintf("char out of alphabet: %q", ch))
		}
		result[i] = s2c[ch]
	}
	return string(result)
}

func Decode(encoded string) ([]byte, error) {
	n := len(encoded)
	mapped := make([]byte, n)
	for i := 0; i < n; i++ {
		ch := encoded[i]
		if ch >= 128 || !c2sOK[ch] {
			return nil, fmt.Errorf("char out of custom alphabet: %q", ch)
		}
		mapped[i] = c2s[ch]
	}
	a := n / 3
	std := make([]byte, n)
	copy(std[:a], mapped[n-a:])
	copy(std[a:n-a], mapped[a:n-a])
	copy(std[n-a:], mapped[:a])
	return base64.StdEncoding.DecodeString(string(std))
}

const appCode = "cosy"
const defaultSecret = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw=="

var secretValue string
var secretOnce sync.Once

func getSecret() string {
	secretOnce.Do(func() {
		secretValue = os.Getenv("QODER_SIGNATURE_SECRET")
		if secretValue == "" {
			secretValue = defaultSecret
		}
	})
	return secretValue
}

func CurrentDate() string {
	return time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
}

func Sign(date string) string {
	s := appCode + "&" + getSecret() + "&" + date
	h := cosySignatureHash.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

const serverPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var rsaPubKey *rsa.PublicKey

func init() {
	block, _ := pem.Decode([]byte(serverPubKeyPEM))
	if block == nil {
		panic("failed to parse server public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("failed to parse server public key: %v", err))
	}
	rsaPubKey = pub.(*rsa.PublicKey)
}

type AuthIdentity struct {
	Name               string
	Aid                string
	UID                string
	YxUID              string
	OrganizationID     string
	OrganizationName   string
	UserType           string
	SecurityOauthToken string
	RefreshToken       string
}

type SessionContext struct {
	TempKey         []byte
	CosyKey         string
	Info            string
	Identity        AuthIdentity
	MachineID       string
	MachineToken    string
	MachineType     string
	MachineCode     string
	MachineOS       string
	MachineHostname string
}

func rsaEncrypt(tempKey []byte) ([]byte, error) {
	return rsa.EncryptPKCS1v15(rand.Reader, rsaPubKey, tempKey) //nolint:staticcheck // COSY 服务器固定使用 PKCS#1 v1.5，改成 OAEP 会破坏协议。
}

func aesEncrypt(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padLen := 16 - len(plain)%16
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	encrypted := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, key[:16])
	mode.CryptBlocks(encrypted, padded)
	return encrypted, nil
}

func md5Hex(s string) string {
	h := cosySignatureHash.Sum([]byte(s))
	return fmt.Sprintf("%x", h)
}

type authPayloadStruct struct {
	Name               string `json:"name"`
	Aid                string `json:"aid"`
	UID                string `json:"uid"`
	YxUID              string `json:"yx_uid"`
	OrganizationID     string `json:"organization_id"`
	OrganizationName   string `json:"organization_name"`
	UserType           string `json:"user_type"`
	SecurityOauthToken string `json:"security_oauth_token"`
	RefreshToken       string `json:"refresh_token"`
}

func authPayloadJSON(identity AuthIdentity) []byte {
	p := authPayloadStruct(identity)
	b, err := marshalNoEscape(p)
	if err != nil {
		panic(err)
	}
	return b
}

func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func NewSession(identity AuthIdentity, machineID, machineToken, machineType string) *SessionContext {
	tempKey := []byte(uuid.New().String()[:16])
	encrypted, err := rsaEncrypt(tempKey)
	if err != nil {
		panic(err)
	}
	cosyKey := base64.StdEncoding.EncodeToString(encrypted)
	aesPlain := authPayloadJSON(identity)
	aesEncrypted, err := aesEncrypt(aesPlain, tempKey)
	if err != nil {
		panic(err)
	}
	info := base64.StdEncoding.EncodeToString(aesEncrypted)
	return &SessionContext{
		TempKey:      tempKey,
		CosyKey:      cosyKey,
		Info:         info,
		Identity:     identity,
		MachineID:    machineID,
		MachineToken: machineToken,
		MachineType:  machineType,
	}
}

func SignRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	s := payloadB64 + "\n" + cosyKey + "\n" + cosyDate + "\n" + body + "\n" + pathWithoutAlgo
	return md5Hex(s)
}

type cosyPayloadStruct struct {
	CosyVersion string `json:"cosyVersion"`
	IdeVersion  string `json:"ideVersion"`
	Info        string `json:"info"`
	RequestID   string `json:"requestId"`
	Version     string `json:"version"`
}

func BuildPayloadB64(info string) string {
	p := cosyPayloadStruct{
		CosyVersion: CosyVersion,
		IdeVersion:  "",
		Info:        info,
		RequestID:   uuid.New().String(),
		Version:     "v1",
	}
	b, err := marshalNoEscape(p)
	if err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func ComposeBearer(payloadB64, sig string) string {
	return "Bearer COSY." + payloadB64 + "." + sig
}

// Headers 对已编码请求体签名，路径去掉 /algo 前缀且不含查询字符串。
func Headers(c *SessionContext, path, body string) http.Header {
	date := strconv.FormatInt(time.Now().Unix(), 10)
	payload := BuildPayloadB64(c.Info)
	bearer := ComposeBearer(payload, SignRequest(payload, c.CosyKey, date, body, strings.TrimPrefix(path, "/algo")))
	h := http.Header{}
	h.Set("Authorization", bearer)
	h.Set("cosy-key", c.CosyKey)
	h.Set("cosy-date", date)
	h.Set("cosy-user", c.Identity.UID)
	h.Set("cosy-machineid", c.MachineID)
	h.Set("cosy-machinetoken", c.MachineToken)
	h.Set("cosy-machinetype", c.MachineType)
	if c.MachineCode != "" {
		h.Set("cosy-machinecode", c.MachineCode)
	}
	if c.MachineHostname != "" {
		h.Set("cosy-machinehostname", c.MachineHostname)
	}
	h.Set("cosy-clienttype", "5")
	h.Set("cosy-version", CosyVersion)
	h.Set("login-version", "v2")
	h.Set("cosy-data-policy", "agree")
	h.Set("cosy-business-product", "cli")
	h.Set("cosy-business-type", "agent")
	h.Set("cosy-scene", "assistant")
	h.Set("Content-Type", "application/json")
	h.Set("Accept-Encoding", "identity")
	h.Set("User-Agent", UserAgent)
	h.Set("cosy-machineos", "x86_64_linux")
	if c.MachineOS != "" {
		h.Set("cosy-machineos", c.MachineOS)
	}
	return h
}
func CredentialHeaders(machineID, machineToken, machineType, machineOS string) http.Header {
	date := CurrentDate()
	h := http.Header{}
	h.Set("date", date)
	h.Set("signature", Sign(date))
	h.Set("appcode", "cosy")
	h.Set("cosy-clienttype", "5")
	h.Set("cosy-version", CosyVersion)
	h.Set("cosy-machineid", machineID)
	h.Set("cosy-machinetoken", machineToken)
	h.Set("cosy-machinetype", machineType)
	h.Set("login-version", "v2")
	h.Set("Content-Type", "application/json")
	h.Set("Accept-Encoding", "identity")
	h.Set("User-Agent", UserAgent)
	h.Set("cosy-machineos", "x86_64_linux")
	if machineOS != "" {
		h.Set("cosy-machineos", machineOS)
	}
	return h
}
