//go:build !unix

package selfupdate

import "errors"

// execFn 这些平台没有 exec；unsupportedReason 已经挡住了一键更新，这里只是让编译通过。
var execFn = func(string, []string, []string) error {
	return errors.New("这个平台不支持原地重启")
}
