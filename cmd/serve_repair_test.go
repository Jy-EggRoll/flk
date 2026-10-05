package cmd

import (
	"encoding/json"
	"testing"

	"github.com/jy-eggroll/flk/internal/output"
)

// 本文件覆盖 /api/repair 与 /api/unlink 共用的定位逻辑（recordLocator + filterRecordTargets）
// 以及两个请求体的 JSON 形态。
//
// 为什么单独为它们写单测：定位是这两个破坏性端点里唯一「可能悄悄选错记录」的地方——
// 一旦按 device/type/字段值定位的口径漂移，端点不会报错，而是去操作另一条记录，
// 属于破坏性操作里最危险的失效模式，必须用断言固定
// 真实文件系统的修复与解除动作本身由端到端验收覆盖（起服务 + curl + ls -l），不在这里重复
//
// 请求体形态也要一并固定：两个请求类型都把定位三元组以匿名内嵌结构承载（为了共用一份字段与判定），
// 匿名内嵌的字段是被 encoding/json 提升后解码的，这个行为一旦被改坏
// （比如有人给内嵌字段加上 json tag，字段名会变成嵌套对象），前端发来的扁平 JSON 就会静默解码成空值，
// 而端点只会报「定位信息不全」——看着像前端 bug，排查成本很高

func TestRecordLocatorLocateOne(t *testing.T) {
	cases := []struct {
		name string
		loc  recordLocator
		want bool
	}{
		{"三元组齐备", recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{"real": "a"}}, true},
		{"缺设备", recordLocator{Type: "symlink", Fields: map[string]string{"real": "a"}}, false},
		{"缺类型", recordLocator{Device: "dev", Fields: map[string]string{"real": "a"}}, false},
		{"缺字段", recordLocator{Device: "dev", Type: "symlink"}, false},
		{"字段为空表", recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.loc.locateOne(); got != tc.want {
				t.Fatalf("locateOne() = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// TestRequestJSONShapes 固定两个请求体的扁平 JSON 形态（内嵌结构必须被提升为顶层字段）
func TestRequestJSONShapes(t *testing.T) {
	t.Run("repair 的全量形态", func(t *testing.T) {
		var req repairRequest
		if err := json.Unmarshal([]byte(`{"all":true}`), &req); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if !req.All {
			t.Fatal("all=true 必须被解析进 All 字段")
		}
		if req.locateOne() {
			t.Fatal("全量形态不该被判定为定位齐备")
		}
	})

	t.Run("repair 的单条形态", func(t *testing.T) {
		var req repairRequest
		raw := `{"device":"dev","type":"symlink","fields":{"real":"/r","fake":"/f"}}`
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if !req.locateOne() {
			t.Fatalf("扁平 JSON %s 必须被解析成可定位的三元组，实得 %+v", raw, req.recordLocator)
		}
		if req.Device != "dev" || req.Type != "symlink" || req.Fields["real"] != "/r" || req.Fields["fake"] != "/f" {
			t.Fatalf("字段解码结果不符: %+v", req.recordLocator)
		}
	})

	t.Run("unlink 的单条形态带 noTrash", func(t *testing.T) {
		var req unlinkRequest
		raw := `{"device":"dev","type":"copy","fields":{"src":"/a","dst":"/b"},"noTrash":true}`
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if !req.locateOne() {
			t.Fatalf("扁平 JSON %s 必须被解析成可定位的三元组，实得 %+v", raw, req.recordLocator)
		}
		if !req.NoTrash {
			t.Fatal("noTrash=true 必须被解析进 NoTrash 字段")
		}
	})

	t.Run("unlink 缺定位信息", func(t *testing.T) {
		var req unlinkRequest
		if err := json.Unmarshal([]byte(`{"noTrash":false}`), &req); err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if req.locateOne() {
			t.Fatal("缺 device/type/fields 时不应被判定为可定位")
		}
	})
}

func TestFilterRecordTargets(t *testing.T) {
	symlink := output.CheckResult{Type: "symlink", Device: "dev", Real: "/r", Fake: "/f", Valid: false}
	hardlink := output.CheckResult{Type: "hardlink", Device: "dev", Prim: "/p", Seco: "/s", Valid: false}
	copyRes := output.CheckResult{Type: "copy", Device: "dev", Src: "/a", Dst: "/b", Valid: false}
	unknown := output.CheckResult{Type: "weird", Device: "dev", Valid: false}
	all := []output.CheckResult{symlink, hardlink, copyRes, unknown}

	cases := []struct {
		name    string
		loc     recordLocator
		wantLen int
		wantTyp string
	}{
		{
			name:    "符号链接按 real/fake 命中",
			loc:     recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{"real": "/r", "fake": "/f"}},
			wantLen: 1, wantTyp: "symlink",
		},
		{
			name:    "硬链接按 prim/seco 命中",
			loc:     recordLocator{Device: "dev", Type: "hardlink", Fields: map[string]string{"prim": "/p", "seco": "/s"}},
			wantLen: 1, wantTyp: "hardlink",
		},
		{
			name:    "副本按 src/dst 命中",
			loc:     recordLocator{Device: "dev", Type: "copy", Fields: map[string]string{"src": "/a", "dst": "/b"}},
			wantLen: 1, wantTyp: "copy",
		},
		{
			// 类型对得上但路径不同：绝不能「差不多就修」，必须零匹配
			name:    "路径不一致不命中",
			loc:     recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{"real": "/r", "fake": "/other"}},
			wantLen: 0,
		},
		{
			name:    "设备不一致不命中",
			loc:     recordLocator{Device: "other", Type: "symlink", Fields: map[string]string{"real": "/r", "fake": "/f"}},
			wantLen: 0,
		},
		{
			name:    "类型不一致不命中",
			loc:     recordLocator{Device: "dev", Type: "copy", Fields: map[string]string{"real": "/r", "fake": "/f"}},
			wantLen: 0,
		},
		{
			// 未知类型拿不到匹配键，宁可不处理也不能退化成「处理第一条」
			name:    "未知类型不命中",
			loc:     recordLocator{Device: "dev", Type: "weird", Fields: map[string]string{"real": "/r"}},
			wantLen: 0,
		},
		{
			// 多给的字段不参与定位，只在记录自身字段上比对
			name:    "多余字段不影响命中",
			loc:     recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{"real": "/r", "fake": "/f", "extra": "x"}},
			wantLen: 1, wantTyp: "symlink",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterRecordTargets(all, tc.loc)
			if len(got) != tc.wantLen {
				t.Fatalf("匹配数 = %d，期望 %d", len(got), tc.wantLen)
			}
			if tc.wantLen > 0 && got[0].Type != tc.wantTyp {
				t.Fatalf("命中类型 = %q，期望 %q", got[0].Type, tc.wantTyp)
			}
		})
	}
}

// TestFilterRecordTargetsDuplicate 重复记录只取第一条：
// 两条记录指向完全相同的路径，逐条执行会让第二条把第一条刚建好（或刚解除）的链接再处理一遍
func TestFilterRecordTargetsDuplicate(t *testing.T) {
	dup := output.CheckResult{Type: "symlink", Device: "dev", Real: "/r", Fake: "/f"}
	got := filterRecordTargets([]output.CheckResult{dup, dup}, recordLocator{Device: "dev", Type: "symlink", Fields: map[string]string{"real": "/r", "fake": "/f"}})
	if len(got) != 1 {
		t.Fatalf("重复记录匹配数 = %d，期望 1", len(got))
	}
}

func TestRecordDisplayPaths(t *testing.T) {
	cases := []struct {
		name   string
		result output.CheckResult
		want   string
	}{
		{"符号链接", output.CheckResult{Type: "symlink", Real: "/r", Fake: "/f"}, "/r → /f"},
		{"硬链接", output.CheckResult{Type: "hardlink", Prim: "/p", Seco: "/s"}, "/p → /s"},
		{"副本", output.CheckResult{Type: "copy", Src: "/a", Dst: "/b"}, "/a → /b"},
		{"未知类型返回空串", output.CheckResult{Type: "weird"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recordDisplayPaths(tc.result); got != tc.want {
				t.Fatalf("recordDisplayPaths() = %q，期望 %q", got, tc.want)
			}
		})
	}
}
