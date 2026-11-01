// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Line 批量导入的一行。
type Line struct {
	// No 第几行（从 1 数，跳过的空行、注释行也算）。
	No int `json:"no"`
	// Text 这一行原文，密码已打码。
	Text string `json:"text"`
	Name string `json:"name,omitempty"`
	// URL 打码后的地址；Error 认不出来的原因（有错的行不导入）。
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
	// Duplicate 和谁重复："existing" 和已有的代理、"batch" 和这批前面的行；DuplicateOf 是那个代理的
	// 名字或那一行的行号。重复的行不导入。
	Duplicate   string `json:"duplicate,omitempty"`
	DuplicateOf string `json:"duplicate_of,omitempty"`

	full string // 规范化后的完整地址（含密码）
}

// parsedProxy parseOne 的结果。
type parsedProxy struct {
	URL  string // 规范化后的完整地址
	Name string // 行尾写的名字，没写为空
}

// ParseLines 解析批量导入的文本：一行一个代理，空行和 # 开头的行跳过；scheme 是没写协议的行用的协议
// （http 或 socks5，别的按 http）。认这几种写法，行尾可以跟空格加名字：
//
//	协议://用户名:密码@主机:端口
//	主机:端口
//	主机:端口:用户名:密码
//	用户名:密码@主机:端口
//	用户名:密码:主机:端口
func ParseLines(text, scheme string) []Line {
	if scheme != "socks5" {
		scheme = "http"
	}
	var out []Line
	seen := map[string]int{}
	for i, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		s := strings.TrimSpace(raw)
		if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
			continue
		}
		l := Line{No: i + 1, Text: maskLine(s)}
		p, err := parseOne(s, scheme)
		if err != nil {
			l.Error = err.Error()
			out = append(out, l)
			continue
		}
		l.full, l.URL, l.Name = p.URL, Masked(p.URL), p.Name
		if l.Name == "" {
			l.Name = hostPort(p.URL)
		}
		key := dedupKey(p.URL)
		if no, ok := seen[key]; ok {
			l.Duplicate, l.DuplicateOf = "batch", strconv.Itoa(no)
		} else {
			seen[key] = l.No
		}
		out = append(out, l)
	}
	return out
}

// parseOne 解析一个代理（可带行尾名字），返回规范化后的地址。
func parseOne(s, scheme string) (parsedProxy, error) {
	addr, name := s, ""
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		addr, name = s[:i], strings.TrimSpace(s[i+1:])
	} else if i := strings.LastIndex(s, "#"); i > 0 {
		addr, name = s[:i], strings.TrimSpace(s[i+1:])
	}
	var u *url.URL
	var err error
	switch {
	case strings.Contains(addr, "://"):
		u, err = url.Parse(addr)
		if err != nil {
			return parsedProxy{}, errors.New("不是合法的代理地址")
		}
	case strings.Contains(addr, "@"):
		u, err = url.Parse(scheme + "://" + addr)
		if err != nil {
			return parsedProxy{}, errors.New("不是合法的代理地址")
		}
	default:
		u, err = colonForm(addr, scheme)
		if err != nil {
			return parsedProxy{}, err
		}
	}
	switch u.Scheme = strings.ToLower(u.Scheme); u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return parsedProxy{}, fmt.Errorf("不支持的协议 %s（只认 http、https、socks5、socks5h）", u.Scheme)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return parsedProxy{}, errors.New("缺主机")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return parsedProxy{}, errors.New("端口不对（要 1～65535 的数字）")
	}
	clean := &url.URL{Scheme: u.Scheme, Host: net.JoinHostPort(strings.ToLower(host), port)}
	if u.User != nil && u.User.Username() != "" {
		if pass, ok := u.User.Password(); ok {
			clean.User = url.UserPassword(u.User.Username(), pass)
		} else {
			clean.User = url.User(u.User.Username())
		}
	}
	return parsedProxy{URL: clean.String(), Name: name}, nil
}

// colonForm 解析冒号分隔的写法：主机:端口、主机:端口:用户名:密码、用户名:密码:主机:端口。
func colonForm(addr, scheme string) (*url.URL, error) {
	parts := strings.Split(addr, ":")
	isPort := func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= 1 && n <= 65535
	}
	looksHost := func(s string) bool {
		return s == "localhost" || net.ParseIP(s) != nil || strings.Contains(s, ".")
	}
	var host, port, user, pass string
	switch {
	case len(parts) == 2:
		host, port = parts[0], parts[1]
	case len(parts) == 4 && isPort(parts[1]) && looksHost(parts[0]):
		host, port, user, pass = parts[0], parts[1], parts[2], parts[3]
	case len(parts) == 4 && isPort(parts[3]) && looksHost(parts[2]):
		user, pass, host, port = parts[0], parts[1], parts[2], parts[3]
	case len(parts) == 4:
		return nil, errors.New("端口不对（要 1～65535 的数字）")
	default:
		return nil, errors.New("认不出写法（支持 协议://用户名:密码@主机:端口、主机:端口、主机:端口:用户名:密码、用户名:密码@主机:端口、用户名:密码:主机:端口）")
	}
	u := &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u, nil
}

// Masked 打码后的地址：密码换成 ***。地址解析不了时原样返回（只会是落盘前已校验过的地址）。
func Masked(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, ok := u.User.Password(); ok {
		return u.Scheme + "://" + u.User.Username() + ":***@" + u.Host
	}
	return u.Scheme + "://" + u.User.Username() + "@" + u.Host
}

// maskLine 给原文打码：认得出的换成打码地址；认不出的，有 @ 就把第一个冒号到 @ 之间打掉，没有 @ 的
// 冒号写法除了第一段和像主机、端口的段都打掉（分不清哪段是密码，宁可多打）。
func maskLine(s string) string {
	if p, err := parseOne(s, "http"); err == nil {
		m := Masked(p.URL)
		if p.Name != "" {
			m += " " + p.Name
		}
		return m
	}
	if i := strings.LastIndex(s, "@"); i > 0 {
		if j := strings.Index(s[:i], ":"); j >= 0 && !strings.HasPrefix(s[j:], "://") {
			return s[:j+1] + "***" + s[i:]
		}
		return s
	}
	parts := strings.Split(s, ":")
	if len(parts) < 3 || strings.Contains(s, "://") {
		return s
	}
	for i := 1; i < len(parts); i++ {
		if _, err := strconv.Atoi(parts[i]); err != nil && net.ParseIP(parts[i]) == nil && !strings.Contains(parts[i], ".") {
			parts[i] = "***"
		}
	}
	return strings.Join(parts, ":")
}

// keepPassword 面板上改地址时密码那段还是打码的 ***，就沿用原来的密码（两个地址都是校验过的）。
func keepPassword(newURL, oldURL string) string {
	nu, _ := url.Parse(newURL)
	ou, _ := url.Parse(oldURL)
	if nu.User == nil || ou.User == nil {
		return newURL
	}
	np, _ := nu.User.Password()
	op, ok := ou.User.Password()
	if np != "***" || !ok {
		return newURL
	}
	nu.User = url.UserPassword(nu.User.Username(), op)
	return nu.String()
}

// hostPort 地址里的 主机:端口。
func hostPort(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Host
}

// dedupKey 去重键：协议 + 用户名 + 主机端口一样就算同一个代理（只改了密码也算重复）。
func dedupKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	return strings.ToLower(u.Scheme) + "://" + user + "@" + strings.ToLower(u.Host)
}

// Preview 解析导入文本并标出和已有代理重复的行，不改任何东西。
func (r *Registry) Preview(text, scheme string) []Line {
	lines := ParseLines(text, scheme)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markExistingLocked(lines)
	return lines
}

func (r *Registry) markExistingLocked(lines []Line) {
	for i := range lines {
		l := &lines[i]
		if l.Error != "" || l.Duplicate != "" {
			continue
		}
		if px := r.proxyByKeyLocked(dedupKey(l.full)); px != nil {
			l.Duplicate, l.DuplicateOf = "existing", px.Name
		}
	}
}

// ImportResult 导入结果。
type ImportResult struct {
	Lines []Line `json:"lines"`
	// Added 新加的代理 ID；有错的行、重复的行不导入。
	Added []string `json:"added"`
}

// Import 把导入文本里能用的行加进池 pool（到期时间 expires 可为 nil），加完马上测一遍，有空位就给
// 等代理的号补上。
func (r *Registry) Import(text, scheme, pool string, expires *time.Time) (ImportResult, error) {
	res := ImportResult{Lines: ParseLines(text, scheme)}
	err := r.change(func(t *tx) error {
		if r.poolLocked(pool) == nil {
			return fmt.Errorf("池 %s %w", pool, ErrNotFound)
		}
		r.markExistingLocked(res.Lines)
		now := r.now()
		for _, l := range res.Lines {
			if l.Error != "" || l.Duplicate != "" {
				continue
			}
			px := &Proxy{ID: newID(), Name: l.Name, URL: l.full, Pool: pool, CreatedAt: now}
			if expires != nil {
				e := *expires
				px.ExpiresAt = &e
			}
			r.proxies = append(r.proxies, px)
			r.due[px.ID] = true
			res.Added = append(res.Added, px.ID)
		}
		if len(res.Added) > 0 {
			t.saveFile, t.kick = true, true
			r.fillWaitingLocked(t)
		}
		return nil
	})
	if err != nil {
		res.Added = nil
	}
	return res, err
}

// Export 导出全部代理（完整地址，含密码），一行一个「地址 名字」，可以原样再导入。
func (r *Registry) Export() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, pl := range r.pools {
		b.WriteString("# 池：" + pl.Name + "\n")
		for _, px := range r.proxies {
			if px.Pool == pl.ID {
				b.WriteString(px.URL + " " + px.Name + "\n")
			}
		}
	}
	return b.String()
}
