// credit_floor_config_test.go pool.credit_floor 配置测试：
// 默认 0（关闭，零回归）/ 文件覆盖 / 负值钳 0 / 大值合法。
package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreditFloorDefault 键缺席 → 默认 0（保底关闭，行为与引入前一致）。
func TestCreditFloorDefault(t *testing.T) {
	c := Default()
	if err := c.normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Pool.CreditFloor != 0 {
		t.Errorf("credit_floor=%d want 0 (default off)", c.Pool.CreditFloor)
	}
}

// TestCreditFloorParsedFromFile 显式配置覆盖默认。
func TestCreditFloorParsedFromFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"pool":{"credit_floor":100}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Pool.CreditFloor != 100 {
		t.Errorf("credit_floor=%d want 100", c.Pool.CreditFloor)
	}
}

// TestCreditFloorNegativeClamped 负值钳 0（非法即关闭，不报错：老配置误写不炸启动）。
func TestCreditFloorNegativeClamped(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"pool":{"credit_floor":-5}}`), 0o600)
	c, err := Load(fp)
	if err != nil {
		t.Fatal(err)
	}
	if c.Pool.CreditFloor != 0 {
		t.Errorf("credit_floor=%d want 0 (negative clamped)", c.Pool.CreditFloor)
	}
}
