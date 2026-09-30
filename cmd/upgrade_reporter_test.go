package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestProgressClearsLineBeforeRedraw 守住「每一帧进度都先清行再重绘」这一契约
//
// 回归背景（用户实测踩到）：进度行的绘制原先是「\r + 内容」，只回到行首而不清到行尾。
// 下载完成的那一帧不再输出速率与剩余时间（见 ptermProgress.Update 里的 done < total 判断），
// 整帧明显短于上一帧，于是上一帧多出来的尾巴留在行尾，终端上叠出「剩余 3s2s」这类伪内容——
// 看起来像剩余时间被算重了，实际是两帧叠加后的残留。
//
// 帧长变化并不只发生在收尾：速率、剩余时间与百分比的字符数都会随进度改变，
// 所以这条契约要求每一帧都清行，而不是「只在完成时清一次」
//
// 断言方式：把 writer 捕获到的输出按 \r 切帧，检查每一帧都以 ANSI 的清行序列开头。
// 残留本身是终端渲染效果，纯字符串层面观察不到；但「有没有清行」是可断言的因，
// 守住它就能保证任何帧长变化都不会留下残影
func TestProgressClearsLineBeforeRedraw(t *testing.T) {
	var buf bytes.Buffer
	reporter := &ptermReporter{writer: &buf}

	progress, ok := reporter.Progress("downloading").(*ptermProgress)
	if !ok {
		t.Fatal("Progress 应返回 *ptermProgress")
	}

	// 三帧覆盖「中途」与「收尾」两种形态：
	// 中途帧带速率与剩余时间，收尾帧两者都没有（因此明显更短）——后者正是残留的制造者
	for _, frame := range [][2]int64{
		{1 << 20, 10 << 20},
		{5 << 20, 10 << 20},
		{10 << 20, 10 << 20},
	} {
		progress.Update(frame[0], frame[1])
	}

	out := buf.String()
	frames := strings.Split(out, "\r")
	if len(frames) < 2 {
		t.Fatalf("应至少重绘两帧，实际输出: %q", out)
	}
	for i, frame := range frames[1:] {
		if !strings.HasPrefix(frame, "\x1b[K") {
			t.Errorf("第 %d 帧没有清行，会留下上一帧的尾巴: %q", i+1, frame)
		}
	}
}
