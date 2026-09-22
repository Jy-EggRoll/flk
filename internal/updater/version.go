package updater

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 版本标签只接受两种形态：正式版 x.y.z 与开发版 x.y.z.dev.n，前缀 v 可选
// 刻意不支持 -rc.1、+build 等预发布与元数据后缀，使发布标签、资产命名与比较规则三者保持唯一对应，
// 避免出现"能发布但升级器看不懂"的标签
var (
	releasePattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
	devPattern     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)\.dev\.(\d+)$`)
)

// Version 是一个语义化版本号
// IsDev 区分正式版与开发版，二者在同号时存在固定的大小关系：开发版永远低于正式版，
// 因为 1.2.3.dev.9 是 1.2.3 正式发布之前的预发布，正式版一经发布就应当被开发版用户视为更新
type Version struct {
	Major int
	Minor int
	Patch int
	Dev   int
	IsDev bool
}

// ParseVersion 解析版本标签原文
// ok 为 false 表示标签不符合任何受支持形态，调用方必须显式跳过该标签
// 这里刻意不返回零值版本兜底：把解析失败当作 0.0.0.dev.0 会让无关标签（如 latest、v1）被误判成一个真实的开发版并参与比较
func ParseVersion(raw string) (Version, bool) {
	trimmed := strings.TrimPrefix(raw, "v")

	if matched := devPattern.FindStringSubmatch(trimmed); matched != nil {
		major, _ := strconv.Atoi(matched[1])
		minor, _ := strconv.Atoi(matched[2])
		patch, _ := strconv.Atoi(matched[3])
		dev, _ := strconv.Atoi(matched[4])
		return Version{Major: major, Minor: minor, Patch: patch, Dev: dev, IsDev: true}, true
	}

	if matched := releasePattern.FindStringSubmatch(trimmed); matched != nil {
		major, _ := strconv.Atoi(matched[1])
		minor, _ := strconv.Atoi(matched[2])
		patch, _ := strconv.Atoi(matched[3])
		return Version{Major: major, Minor: minor, Patch: patch}, true
	}

	return Version{}, false
}

// Compare 按主次修订号与开发序号依次比较，返回值语义与 cmp.Compare 一致：
// 小于 0 表示 v 更旧，大于 0 表示 v 更新，0 表示等价
// 同号时开发版低于正式版这一条规则是内建的，因此调用方只需一次 Compare 就能同时完成
// "是否更新"与"不会把正式版用户降级到同号开发版"两个判断，无需额外特判
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		return cmp.Compare(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return cmp.Compare(v.Minor, other.Minor)
	}
	if v.Patch != other.Patch {
		return cmp.Compare(v.Patch, other.Patch)
	}
	if v.IsDev != other.IsDev {
		if v.IsDev {
			return -1
		}
		return 1
	}
	if v.IsDev {
		return cmp.Compare(v.Dev, other.Dev)
	}
	return 0
}

// String 输出规范化的版本号，用于日志与错误信息，便于用户直接对照发布标签
func (v Version) String() string {
	if v.IsDev {
		return fmt.Sprintf("%d.%d.%d.dev.%d", v.Major, v.Minor, v.Patch, v.Dev)
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}
