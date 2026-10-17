// Package wb2api 放仓库根目录下需要打进二进制的文件：go:embed 不能引用上级目录，
// docs/ 里的文件只能从根包嵌入。
package wb2api

import _ "embed"

// Changelog 更新日志（docs/CHANGELOG.md）原文。面板升级后的「本次更新」弹窗从这里取条目，
// 发版时 CI 也从同一份文件提取 Release 说明。
//
//go:embed docs/CHANGELOG.md
var Changelog string
