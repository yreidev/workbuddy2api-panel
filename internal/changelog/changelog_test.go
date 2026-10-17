package changelog

import (
	"reflect"
	"testing"
)

const sample = `# 更新日志

开头的说明不属于任何版本。

## [未发布]

- 未发布的条目不出现

## [1.1.0] - 2026-11-02

这一版的简介，
接着一行。

### 新增

- 新功能 ` + "`a.b`" + `
  续行并入上一条
- 第二条

### 修复

* 星号开头也算条目

<!-- 注释忽略 -->

## [1.0.1] - 2026-10-20

- 没有分组的条目

## [1.2.0-rc.1] - 2026-11-10

### 新增

- 预发布

## [1.0] - 2026-10-01

- 不完整的版本号整段忽略

## [1.0.0] - 2026-10-17

### 新增

- 首版

[1.1.0]: https://example.com/compare/v1.0.1...v1.1.0
`

func versions(rels []Release) []string {
	out := []string{}
	for _, r := range rels {
		out = append(out, r.Version)
	}
	return out
}

func TestParse(t *testing.T) {
	rels := Parse(sample)
	if got, want := versions(rels), []string{"1.2.0-rc.1", "1.1.0", "1.0.1", "1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	r := rels[1]
	if r.Date != "2026-11-02" || r.Summary != "这一版的简介， 接着一行。" {
		t.Fatalf("1.1.0 date/summary = %q / %q", r.Date, r.Summary)
	}
	want := []Group{
		{Title: "新增", Items: []string{"新功能 `a.b` 续行并入上一条", "第二条"}},
		{Title: "修复", Items: []string{"星号开头也算条目"}},
	}
	if !reflect.DeepEqual(r.Groups, want) {
		t.Fatalf("1.1.0 groups = %#v", r.Groups)
	}
	if g := rels[2].Groups; len(g) != 1 || g[0].Title != "" || !reflect.DeepEqual(g[0].Items, []string{"没有分组的条目"}) {
		t.Fatalf("1.0.1 groups = %#v", g)
	}
	if g := rels[3].Groups; len(g) != 1 || !reflect.DeepEqual(g[0].Items, []string{"首版"}) {
		t.Fatalf("1.0.0 groups = %#v（链接定义不能并进条目）", g)
	}
}

func TestParseCRLF(t *testing.T) {
	rels := Parse("## [1.0.0] - 2026-10-17\r\n\r\n### 新增\r\n\r\n- 首版\r\n")
	if len(rels) != 1 || rels[0].Date != "2026-10-17" || rels[0].Groups[0].Items[0] != "首版" {
		t.Fatalf("CRLF parse = %#v", rels)
	}
}

func TestValidAndCompare(t *testing.T) {
	for v, ok := range map[string]bool{"1.0.0": true, "1.2.0-rc.1": true, "v1.0.0": false, "1.0": false, "1.0.0+build": false, "": false} {
		if Valid(v) != ok {
			t.Errorf("Valid(%q) = %v, want %v", v, !ok, ok)
		}
	}
	cases := []struct {
		a, b string
		want int
	}{
		{"1.10.0", "1.9.0", 1},
		{"1.0.0", "1.0.0", 0},
		{"1.1.0-rc.1", "1.1.0", -1},
		{"1.1.0-rc.1", "1.0.9", 1},
		{"bogus", "0.0.1", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestStable(t *testing.T) {
	if !Prerelease("1.2.0-rc.1") || Prerelease("1.2.0") {
		t.Fatal("Prerelease")
	}
	if got, want := versions(Stable(Parse(sample))), []string{"1.1.0", "1.0.1", "1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Stable = %v, want %v", got, want)
	}
}

func TestNewerAndBetween(t *testing.T) {
	rels := Parse(sample)
	if got, want := versions(Newer(rels, "1.0.1")), []string{"1.2.0-rc.1", "1.1.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Newer = %v, want %v", got, want)
	}
	if got := Newer(rels, "1.2.0-rc.1"); len(got) != 0 {
		t.Fatalf("Newer(latest) = %v, want none", versions(got))
	}
	if got, want := versions(Between(rels, "1.0.0", "1.1.0")), []string{"1.1.0", "1.0.1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Between = %v, want %v", got, want)
	}
	// 降级或没变：没有「本次更新」
	if got := Between(rels, "1.1.0", "1.0.0"); len(got) != 0 {
		t.Fatalf("Between(downgrade) = %v", versions(got))
	}
	if got := Between(rels, "1.1.0", "1.1.0"); len(got) != 0 {
		t.Fatalf("Between(same) = %v", versions(got))
	}
}
