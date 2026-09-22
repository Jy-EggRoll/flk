package updater

import (
	"fmt"
	"path/filepath"
	"strings"
)

// upgradeScriptDelaySeconds 是脚本等待当前进程退出的秒数
// Windows 无法覆盖正在运行的映像文件，只能等进程结束后由外部脚本完成替换，
// 该延时就是留给进程退出的窗口
//
// 这里保留长期验证过的 timeout，不要改用 ping 之类的替代方案：
// 那类做法在禁用 ICMP 或拦截环回请求的环境下会立即返回，等于取消等待，
// 反而让本来可用的替换失败
const upgradeScriptDelaySeconds = 1

// windowsUpgradePlan 描述 Windows 延迟替换所需的全部产物
type windowsUpgradePlan struct {
	// ScriptPath 是脚本落盘位置，位于安装目录内以便随程序一起被找到
	ScriptPath string

	// Script 是脚本内容
	Script string

	// KeepPath 是替换失败时新版本的保留位置
	KeepPath string
}

// planWindowsUpgrade 生成用于延迟替换可执行文件的批处理脚本
//
// Windows 不允许覆盖或删除正在运行的映像文件，替换动作只能交给一个独立于当前进程的脚本，
// 在进程退出之后执行；这是与 Unix 的根本差异，后者直接重命名覆盖即可，完全不需要脚本
//
// 脚本主体沿用长期验证过的做法（等待、复制、清理、自删），只在其后追加一层失败检测：
// 一旦复制没能成功，就把已下载的新版本保留在旁边并给出提示，
// 避免用户既没升级成功、也不知道下载好的文件去了哪里
//
// 该函数被放在平台无关文件中，是为了让脚本内容能在任意平台上被测试覆盖：
// 替换行为本身无法在非 Windows 环境运行验证，但脚本的构成至少应当被自动化断言锁住
func planWindowsUpgrade(staged, execPath string) windowsUpgradePlan {
	dir := filepath.Dir(execPath)
	base := strings.TrimSuffix(filepath.Base(execPath), ".exe")
	keepPath := execPath + ".new"

	// 等待与复制两行保持原样，不做任何"优化"：
	// 这段脚本是 Windows 升级唯一被实际验证过的实现，正常路径必须逐字保持不变
	script := fmt.Sprintf(`@echo off
timeout /t %d /nobreak >nul
copy /Y "%s" "%s" >nul
if errorlevel 1 (
  move /Y "%s" "%s" >nul 2>&1
  echo Upgrade failed: the running executable could not be replaced.
  echo The downloaded version was kept as "%s".
  echo Please close the program and replace it manually.
  del "%%~f0" >nul 2>&1
  exit /b 1
)
del "%s" >nul 2>&1
del "%%~f0" >nul 2>&1
`, upgradeScriptDelaySeconds, staged, execPath, staged, keepPath, keepPath, staged)

	return windowsUpgradePlan{
		ScriptPath: filepath.Join(dir, base+"-upgrade.bat"),
		Script:     script,
		KeepPath:   keepPath,
	}
}
