//go:build windows

package updater

// replaceExecutable 在 Windows 上通过「原地改名交接」替换当前可执行文件
//
// 旧实现在这里写一个批处理脚本、等进程退出后再由脚本复制覆盖。那条路在安装目录含非 ASCII 字符时
// 会静默失败（脚本按 UTF-8 落盘、cmd.exe 按本地代码页读取，路径被解码成乱码），
// 而且脚本是「丢出去就不管」的，主进程会误报「升级完成」——用户直到下次看版本号才发现没换上。
// 完整的失败过程与取舍见 swapExecutable 的注释
//
// 与 Unix 的差别仍然存在，但不再需要外部脚本：Windows 只禁止覆盖运行中的映像，不禁止给它改名，
// 因此改名交接就足以在本进程尚未退出时完成替换
func replaceExecutable(staged, execPath string) error {
	// 固定用同一个后辍名而不是每次生成新名：本进程仍在占用这个文件，此刻删不掉，
	// 固定名字让残留至多一份、并被下一次升级自然覆盖，而不是随升级次数累积
	return swapExecutable(staged, execPath, execPath+".old")
}
