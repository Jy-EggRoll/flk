// atomicfile 提供「同目录临时文件 + rename」的原子落盘，供项目内所有写入用户数据的
// 纯文本文件复用（当前的调用方是设置文件与清单文件）。
//
// 为什么值得单独成一个包：这两份文件的写入要求完全一致，此前在两个包里各写了一遍，
// 而且清单那一份实际上没有做到原子——它直接用 os.WriteFile 截断写，进程在写入中途
// 退出（崩溃、磁盘写满、断电）就会留下一个被截断的 JSON。清单是用户链接记录的唯一
// 存放处，损坏后 flk 的每条命令都会启动失败，且没有第二份副本可恢复。因此把这条策略
// 收口到一处，任何写入用户数据的地方都走这里，避免日后又在某个新写入点重新踩一遍。
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write 原子地把 data 写入 path，成功返回 nil，失败时不会留下临时文件
//
// 三条约束决定了实现形态，缺一不可：
//
//  1. **目标可能是符号链接**：用户可以把设置文件或清单文件链进自己的配置仓库，
//     这是本项目推荐的用法。必须让写入落在链接指向的真实文件上——本函数最终用 rename
//     落位，而 rename 替换的是「路径上的那个名字」，直接写链接路径会把链接本身换成
//     普通文件，用户的仓库与 ~/.config 下的入口从此脱钩，直到下次同步才发现两边各写各的。
//     两条解析路径覆盖两种链接形态：目标是已存在的文件时走 EvalSymlinks，它会把整条链
//     （含链上的目录链接）都解到底；目标是断链（还没被创建——例如刚把文件链进配置仓库，
//     仓库侧那份还没写）时 EvalSymlinks 会报错，此时不能用它，改为手工沿链接走一步到目标
//     路径，让首次写入**落在用户指定的位置**并保持链接完好。若直接沿用链接自身的路径，
//     rename 会把链接替换成普通文件，用户「把配置放进仓库」的意图就被静默推翻了。
//     只走一步：多级断链（链接指向另一个断链）会在这里停下并把文件建在下一跳的位置上，
//     结果仍是「文件出现在用户链路的下一个节点」，不会写坏别处
//
//  2. **临时文件必须与目标同目录**：rename 只在同一文件系统内才是原子的，把临时文件放到
//     系统临时目录会退化成「复制 + 删除」，崩溃窗口依然存在
//
//  3. **顺序必须是 Write → Sync → Close → Chmod → Rename**：rename 只保证「文件名替换」
//     这一步原子，不保证数据已落盘；缺了 Sync，断电后可能出现「新文件名 + 空内容」，
//     效果等同于清空用户数据
//
// 已知边界（本函数刻意接受的代价）：rename 落位会换掉 inode，因此目标文件原有的自定义
// 属主、ACL、xattr / SELinux 上下文不会被继承，代码只对齐了权限位。若用户不是用符号链接
// 而是用**硬链接**把这份文件接进仓库，写入后链接会与其源文件脱钩。原子性与这些元数据
// 之间没有两全的做法，本项目选择原子性，因为它们比元数据更常被破坏、后果也更重
//
// 潜在影响点：path 的父目录不存在时会被按需创建（首次运行、或用户把文件放到新位置）；
// 目标已存在时权限位沿用它原有的值，首次创建时为 0644（这些文件里都没有密钥，
// 但也没必要因为一次写回而改变用户既有的权限设置）
func Write(path string, data []byte) error {
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
		path = resolved
	} else if target, linkErr := os.Readlink(path); linkErr == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}

	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// 模板里的 * 会被 CreateTemp 替换成随机串，临时文件因此不会与并发的另一次写入重名
	tmp, err := os.CreateTemp(dir, ".flk-atomic-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	// 失败路径统一清理临时文件：留着它既污染用户目录，也会让后续排查的人
	// 误以为「有一份没写完的数据」。成功路径上它已经被 rename 掉，Remove 会失败但无妨
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	// CreateTemp 建出来的文件是 0600，落位前显式对齐目标原有的权限位
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode().Perm()
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return err
	}

	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}
