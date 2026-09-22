package updater

import "testing"

// TestParseVersion 覆盖受支持与不受支持的版本标签形态
// 重点在于不受支持的标签必须被明确拒绝：早期实现把解析失败当作 0.0.0.dev.0，
// 会让 latest 这类无关标签参与版本比较
func TestParseVersion(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   Version
		wantOK bool
	}{
		{name: "正式版", raw: "1.2.3", want: Version{Major: 1, Minor: 2, Patch: 3}, wantOK: true},
		{name: "带 v 前缀的正式版", raw: "v1.2.3", want: Version{Major: 1, Minor: 2, Patch: 3}, wantOK: true},
		{name: "大版本号", raw: "v10.20.30", want: Version{Major: 10, Minor: 20, Patch: 30}, wantOK: true},
		{name: "开发版", raw: "1.2.3.dev.4", want: Version{Major: 1, Minor: 2, Patch: 3, Dev: 4, IsDev: true}, wantOK: true},
		{name: "带 v 前缀的开发版", raw: "v1.2.3.dev.4", want: Version{Major: 1, Minor: 2, Patch: 3, Dev: 4, IsDev: true}, wantOK: true},
		{name: "开发序号为零", raw: "1.2.3.dev.0", want: Version{Major: 1, Minor: 2, Patch: 3, IsDev: true}, wantOK: true},

		{name: "本地构建占位版本", raw: "dev", wantOK: false},
		{name: "空字符串", raw: "", wantOK: false},
		{name: "缺少修订号", raw: "1.2", wantOK: false},
		{name: "缺少开发序号", raw: "1.2.3.dev", wantOK: false},
		{name: "多余段", raw: "1.2.3.4", wantOK: false},
		{name: "带预发布后缀", raw: "1.2.3-rc.1", wantOK: false},
		{name: "非数字段", raw: "latest", wantOK: false},
		{name: "前后有空白", raw: " 1.2.3", wantOK: false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := ParseVersion(testCase.raw)
			if ok != testCase.wantOK {
				t.Fatalf("ParseVersion(%q) ok = %v，期望 %v", testCase.raw, ok, testCase.wantOK)
			}
			if !testCase.wantOK {
				return
			}
			if got != testCase.want {
				t.Fatalf("ParseVersion(%q) = %+v，期望 %+v", testCase.raw, got, testCase.want)
			}
		})
	}
}

// TestVersionCompare 固化版本大小规则
// 其中"同号开发版低于正式版"是升级正确性的关键：它保证正式版用户不会被"升级"到 1.2.3.dev.9，
// 同时保证开发版用户在 1.2.3 正式发布后能收到升级
func TestVersionCompare(t *testing.T) {
	mustParse := func(raw string) Version {
		version, ok := ParseVersion(raw)
		if !ok {
			t.Fatalf("测试用例使用了无法解析的版本 %q", raw)
		}
		return version
	}

	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "修订号更大", left: "1.2.4", right: "1.2.3", want: 1},
		{name: "修订号更小", left: "1.2.3", right: "1.2.4", want: -1},
		{name: "完全相等", left: "1.2.3", right: "1.2.3", want: 0},
		{name: "主版本优先", left: "2.0.0", right: "1.9.9", want: 1},
		{name: "次版本优先", left: "1.3.0", right: "1.2.9", want: 1},
		{name: "同号开发版低于正式版", left: "1.2.3.dev.9", right: "1.2.3", want: -1},
		{name: "正式版高于同号开发版", left: "1.2.3", right: "1.2.3.dev.9", want: 1},
		{name: "开发序号比较", left: "1.2.3.dev.6", right: "1.2.3.dev.5", want: 1},
		{name: "开发序号相等", left: "1.2.3.dev.5", right: "1.2.3.dev.5", want: 0},
		{name: "高号开发版高于低号正式版", left: "1.2.4.dev.1", right: "1.2.3", want: 1},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := mustParse(testCase.left).Compare(mustParse(testCase.right))
			// 只校验符号，具体数值对调用方无意义
			if sign(got) != testCase.want {
				t.Fatalf("%s.Compare(%s) = %d，期望符号 %d", testCase.left, testCase.right, got, testCase.want)
			}
		})
	}
}

// TestVersionString 验证规范化输出与输入标签保持一致，便于用户对照发布标签排查问题
func TestVersionString(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "1.2.3", want: "1.2.3"},
		{raw: "v1.2.3", want: "1.2.3"},
		{raw: "v1.2.3.dev.4", want: "1.2.3.dev.4"},
	}

	for _, testCase := range tests {
		version, ok := ParseVersion(testCase.raw)
		if !ok {
			t.Fatalf("解析 %q 失败", testCase.raw)
		}
		if got := version.String(); got != testCase.want {
			t.Fatalf("Version(%q).String() = %q，期望 %q", testCase.raw, got, testCase.want)
		}
	}
}

// sign 把比较结果归一化为 -1、0、1
func sign(value int) int {
	switch {
	case value < 0:
		return -1
	case value > 0:
		return 1
	default:
		return 0
	}
}
