package updater

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPlanWindowsUpgrade 固化 Windows 延迟替换脚本的关键构成
// 该平台的替换行为无法在当前环境运行验证，因此至少用断言锁住脚本必须包含的要素：
// 等待进程退出、执行替换、失败时保留新版本、脚本自删
func TestPlanWindowsUpgrade(t *testing.T) {
	execPath := filepath.Join("install", "flk.exe")
	staged := filepath.Join("install", ".upgrade-123")

	plan := planWindowsUpgrade(staged, execPath)

	// 脚本与程序同目录，且文件名去掉 .exe 后带 -upgrade 标记
	if want := filepath.Join("install", "flk-upgrade.bat"); plan.ScriptPath != want {
		t.Fatalf("脚本路径 = %q，期望 %q", plan.ScriptPath, want)
	}
	if want := execPath + ".new"; plan.KeepPath != want {
		t.Fatalf("失败保留路径 = %q，期望 %q", plan.KeepPath, want)
	}

	for _, fragment := range []string{
		// 等待与复制两行是 Windows 升级唯一被实际验证过的实现，必须逐字保留
		"timeout /t 1 /nobreak >nul",
		"copy /Y",         // 替换动作
		"if errorlevel 1", // 替换失败检测
		"move /Y",         // 失败时把已下载的新版本挪到保留路径
		`del "%~f0"`,      // 脚本自删，不在安装目录留下残留
		staged,            // 待安装的新版本
		execPath,          // 替换目标
	} {
		if !strings.Contains(plan.Script, fragment) {
			t.Fatalf("脚本缺少关键片段 %q:\n%s", fragment, plan.Script)
		}
	}

	// 正常路径的语句顺序必须保持等待、复制、清理的原始次序，
	// 失败检测只能插在复制之后，不能改变成功时的执行序列
	waitAt := strings.Index(plan.Script, "timeout /t 1")
	copyAt := strings.Index(plan.Script, "copy /Y")
	cleanupAt := strings.Index(plan.Script, `del "`)
	if !(waitAt < copyAt && copyAt < cleanupAt) {
		t.Fatalf("脚本语句顺序异常（等待 %d，复制 %d，清理 %d）:\n%s", waitAt, copyAt, cleanupAt, plan.Script)
	}
}
