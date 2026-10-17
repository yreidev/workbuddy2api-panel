// Package changelog 解析 docs/CHANGELOG.md（Keep a Changelog 格式），并按语义化版本比较版本号。
//
// 只认面板需要的结构：「## [x.y.z] - 日期」开一个版本，「### 分组」开一个分组，「- 」开头的行是
// 条目（缩进的续行并入上一条），版本标题和第一个分组之间的文字是这一版的简介。文件开头的说明、
// [未发布] 这类不是合法版本号的标题、文末的链接定义都忽略。
package changelog

import (
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

// Group 一个分组（新增 / 变更 / 修复……）及其条目。
type Group struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
}

// Release 一个版本的日志。
type Release struct {
	Version string  `json:"version"`
	Date    string  `json:"date,omitempty"`
	Summary string  `json:"summary,omitempty"`
	Groups  []Group `json:"groups"`
}

var (
	releaseHeading = regexp.MustCompile(`^##\s+\[([^\]]+)\](?:\s+-\s+(\S+))?`)
	linkDef        = regexp.MustCompile(`^\[[^\]]+\]:\s`)
)

// Parse 解析更新日志，按版本从新到旧返回。
func Parse(md string) []Release {
	var (
		out     []Release
		cur     *Release
		summary []string
	)
	flush := func() {
		if cur != nil {
			cur.Summary = strings.Join(summary, " ")
			out = append(out, *cur)
		}
		cur, summary = nil, nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "## "):
			flush()
			if m := releaseHeading.FindStringSubmatch(line); m != nil && Valid(m[1]) {
				cur = &Release{Version: m[1], Date: m[2]}
			}
		case cur == nil, trimmed == "", strings.HasPrefix(trimmed, "<!--"), linkDef.MatchString(trimmed):
		case strings.HasPrefix(line, "### "):
			cur.Groups = append(cur.Groups, Group{Title: strings.TrimSpace(line[4:])})
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			if len(cur.Groups) == 0 {
				cur.Groups = append(cur.Groups, Group{})
			}
			g := &cur.Groups[len(cur.Groups)-1]
			g.Items = append(g.Items, trimmed[2:])
		case (line[0] == ' ' || line[0] == '\t') && len(cur.Groups) > 0 && len(cur.Groups[len(cur.Groups)-1].Items) > 0:
			// 缩进的续行：并入上一条
			items := cur.Groups[len(cur.Groups)-1].Items
			items[len(items)-1] += " " + trimmed
		case len(cur.Groups) == 0:
			summary = append(summary, trimmed)
		}
	}
	flush()
	sort.SliceStable(out, func(i, j int) bool { return Compare(out[i].Version, out[j].Version) > 0 })
	return out
}

// Valid 报告 v 是不是完整的语义化版本号（x.y.z，可带预发布后缀，不带 v 前缀）。
func Valid(v string) bool {
	return semver.IsValid("v"+v) && semver.Canonical("v"+v) == "v"+v
}

// Prerelease 报告 v 是不是预发布版本（带 -rc.1 这类后缀）。
func Prerelease(v string) bool { return semver.Prerelease("v"+v) != "" }

// Stable 去掉预发布版本。
func Stable(rels []Release) []Release {
	var out []Release
	for _, r := range rels {
		if !Prerelease(r.Version) {
			out = append(out, r)
		}
	}
	return out
}

// Compare 按语义化版本比较 a 和 b，返回 -1、0 或 1；不合法的版本号比任何合法版本都小。
func Compare(a, b string) int { return semver.Compare("v"+a, "v"+b) }

// Newer 返回比 v 新的版本（rels 须已从新到旧排好，Parse 的结果即是）。
func Newer(rels []Release, v string) []Release {
	var out []Release
	for _, r := range rels {
		if Compare(r.Version, v) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Between 返回比 from 新、且不比 to 新的版本，即从 from 升级到 to 所包含的版本。
func Between(rels []Release, from, to string) []Release {
	var out []Release
	for _, r := range Newer(rels, from) {
		if Compare(r.Version, to) <= 0 {
			out = append(out, r)
		}
	}
	return out
}
