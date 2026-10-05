package cmd

import (
	"strings"
	"testing"
)

// 本文件覆盖 serve 命令自身的定义契约（子命令已并入 serve 之后才成立的那些事实）
//
// 为什么不把这些断言放进 main_test.go：那里跑的是真实子进程，一旦 serve 的参数校验被写坏，
// `flk serve config` 会启动一个服务并永久阻塞，用例只能等 go test 的整体超时，排查成本极高；
// 这里直接在命令树对象上断言，毫秒级失败且不会挂死

// TestServeCmdRejectsExtraArgs 守住 serve 必须显式拒绝位置参数
//
// 回归背景（本次实测踩到）：serve 去掉 config 子命令后变成「无子命令的命令」，
// cobra 对这类命令的默认参数校验是 ArbitraryArgs —— 多余参数被静默忽略并照常启动服务，
// 于是已被移除的 `flk serve config` 会变成「启动服务、把 config 当空气」，
// 用户以为用的还是旧入口，实际行为已完全不同。这类静默降级比直接报错危险得多，
// 因此这里既要求 Args 被显式声明，也要求它真的能拒绝
func TestServeCmdRejectsExtraArgs(t *testing.T) {
	if serveCmd.Args == nil {
		t.Fatal("serveCmd 未声明 Args 校验：`flk serve config` 会被静默忽略并启动服务")
	}
	if err := serveCmd.Args(serveCmd, []string{"config"}); err == nil {
		t.Error("serveCmd 应拒绝位置参数 config")
	}
	if err := serveCmd.Args(serveCmd, nil); err != nil {
		t.Errorf("无位置参数时应通过: %v", err)
	}
	// 旧入口的三个别名要给「已并入 serve」的引导，而不是笼统的 unknown command：
	// 它们曾是这个命令的子命令，改名后敲它们的人需要知道那个入口去哪了
	for _, legacy := range []string{"config", "cfg", "c"} {
		err := serveCmd.Args(serveCmd, []string{legacy})
		if err == nil {
			t.Errorf("serveCmd 应拒绝旧入口别名 %q", legacy)
			continue
		}
		if !strings.Contains(err.Error(), "merged into serve") {
			t.Errorf("旧入口别名 %q 的报错应引导用户改用 flk serve，实得: %v", legacy, err)
		}
	}
	// 非旧入口的普通参数不应被套上引导文案，保持 cobra 的默认措辞
	other := serveCmd.Args(serveCmd, []string{"whatever"})
	if other == nil || strings.Contains(other.Error(), "merged into serve") {
		t.Errorf("普通位置参数的报错不应带上旧入口引导，实得: %v", other)
	}
}

// TestServeCmdFlagOwnership 守住四个参数归属 serve 自身，而不是 serve 的持久化 flag
//
// 回归背景：这四个 flag 原先是 serveCmd 的 PersistentFlags（因为真正干活的 serve config 是它的子命令），
// 子命令并入后必须改用普通 Flags —— 语义上 serve 已无子命令，用持久化声明既名不副实，
// 又会让帮助里的 Local Flags 段落错位。本用例同时固定「没有遗漏的持久化残留」与「四个 flag 都在」
func TestServeCmdFlagOwnership(t *testing.T) {
	for _, name := range []string{"port", "host", "allow-host", "no-open"} {
		if serveCmd.Flags().Lookup(name) == nil {
			t.Errorf("serve 缺少 flag --%s", name)
		}
		if serveCmd.PersistentFlags().Lookup(name) != nil {
			t.Errorf("--%s 仍声明为持久化 flag，serve 已无子命令，应改用 Flags 声明", name)
		}
	}
}

// TestServeCmdAliases 守住别名集合：保留 server，移除 config/cfg/c
//
// 回归背景：旧入口的三个别名都指向「另一个命令」，保留任意一个都会让用户敲出
// 一个看似有效、实则落到 `flk serve` 的调用；项目处于开发验证期，破坏性更新直接改，
// 不做兼容性保留，因此这里断言它们确实已经消失
func TestServeCmdAliases(t *testing.T) {
	has := func(alias string) bool {
		for _, item := range serveCmd.Aliases {
			if item == alias {
				return true
			}
		}
		return false
	}
	if !has("server") {
		t.Error("serve 应保留 server 别名")
	}
	for _, alias := range []string{"config", "cfg", "c"} {
		if has(alias) {
			t.Errorf("serve 不应再保留 %q 别名（旧入口已移除）", alias)
		}
	}
	// 子命令已并入 serve，命令树里不应再有 config 子命令
	for _, child := range serveCmd.Commands() {
		if child.Name() == "config" {
			t.Error("serve 下不应再存在 config 子命令")
		}
	}
}
