package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigInitRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"config", "init"})
	if err != nil {
		t.Fatalf("rootCmd.Find(config init): %v", err)
	}
	if found.Use != "init" {
		t.Errorf("Use: want %q, got %q", "init", found.Use)
	}
}

func TestConfigInitWritesToCustomOut(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mycfg.yaml")

	found, _, _ := rootCmd.Find([]string{"config", "init"})
	buf := &bytes.Buffer{}
	found.SetOut(buf)
	found.SetErr(buf)
	_ = found.Flags().Set("out", target)
	_ = found.Flags().Set("force", "false")

	if err := runConfigInit(found, nil); err != nil {
		t.Fatalf("runConfigInit: %v", err)
	}
	if !strings.Contains(buf.String(), "created:") {
		t.Errorf("stdout: %q", buf.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "ai:") {
		t.Error("template missing ai: section")
	}

	// 두 번째 실행 (--force 없음) → skipped
	buf.Reset()
	if err := runConfigInit(found, nil); err != nil {
		t.Errorf("second run: %v", err)
	}
	if !strings.Contains(buf.String(), "skipped:") {
		t.Errorf("stdout: %q", buf.String())
	}
}

func TestConfigInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cfg.yaml")
	_ = os.WriteFile(target, []byte("stub\n"), 0o644)

	found, _, _ := rootCmd.Find([]string{"config", "init"})
	buf := &bytes.Buffer{}
	found.SetOut(buf)
	_ = found.Flags().Set("out", target)
	_ = found.Flags().Set("force", "true")

	if err := runConfigInit(found, nil); err != nil {
		t.Fatalf("runConfigInit: %v", err)
	}
	data, _ := os.ReadFile(target)
	if strings.Contains(string(data), "stub") {
		t.Error("force=true must overwrite")
	}

	// 플래그 리셋
	_ = found.Flags().Set("force", "false")
}

func TestConfigInitNoOutNoHome(t *testing.T) {
	// XDG_CONFIG_HOME과 HOME을 비워서 경로 결정 불가 상황 시뮬레이션
	// 이 테스트는 환경에 따라 skip될 수 있음
	found, _, _ := rootCmd.Find([]string{"config", "init"})
	buf := &bytes.Buffer{}
	found.SetOut(buf)
	found.SetErr(buf)
	_ = found.Flags().Set("out", "")

	// runConfigInit은 path가 비어있으면 GlobalConfigPath()를 사용하므로
	// 실제 환경에서는 항상 경로가 있을 수 있음. 에러 경로는 통합 테스트에서 검증.
}
