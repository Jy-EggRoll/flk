package output

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/pterm/pterm"
)

// OutputFormat 输出格式类型
type OutputFormat string

const (
	JSON  OutputFormat = "json"
	Table OutputFormat = "table"
)

// CheckResult 单个链接的检查结果
type CheckResult struct {
	Type      string `json:"type"`
	Device    string `json:"device"`
	Real      string `json:"real,omitempty"`
	Fake      string `json:"fake,omitempty"`
	Prim      string `json:"prim,omitempty"`
	Seco      string `json:"seco,omitempty"`
	Src       string `json:"src,omitempty"`
	Dst       string `json:"dst,omitempty"`
	Valid     bool   `json:"valid"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"error_type,omitempty"`
}

// CreateResult 创建结果
type CreateResult struct {
	Success bool   `json:"success"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// toSortedMap 将结构体转为 map[string]interface{}，利用 Go 对 map key 的默认排序实现字典序 JSON 输出
// 支持 json tag 命名和 omitempty 语义
func toSortedMap(v interface{}) map[string]interface{} {
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	typ := val.Type()
	if typ.Kind() != reflect.Struct {
		return nil
	}

	result := make(map[string]interface{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldVal := val.Field(i)

		if field.PkgPath != "" {
			continue
		}

		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := field.Name
		omitEmpty := false
		if tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] != "" {
				name = parts[0]
			}
			for _, p := range parts[1:] {
				if p == "omitempty" {
					omitEmpty = true
					break
				}
			}
		}

		if omitEmpty && fieldVal.IsZero() {
			continue
		}

		result[name] = fieldVal.Interface()
	}
	return result
}

// resultMapValues 从 struct 转换得到的 map 中提取所有值并按键排序，用于排序比较
func resultMapValues(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]string, 0, len(m))
	for _, k := range keys {
		vals = append(vals, fmt.Sprintf("%v", m[k]))
	}
	sort.Strings(vals)
	return vals
}

// PrintCheckResults 打印检查结果
func PrintCheckResults(format OutputFormat, results []CheckResult) error {
	// 收集错误类型并打印解释
	errorTypes := map[string]string{
		"PATH_EXPAND_FAIL":   "路径展开失败",
		"LINK_MISSING":       "链接文件缺失",
		"LINK_ACCESS_FAIL":   "链接访问失败",
		"NOT_SYMLINK":        "不是符号链接",
		"READLINK_FAIL":      "读取链接失败",
		"TARGET_MISSING":     "目标文件缺失",
		"TARGET_ACCESS_FAIL": "目标访问失败",
		"EXPECTED_MISSING":   "期望文件缺失",
		"EXPECTED_ACCESS_FAIL": "期望访问失败",
		"TARGET_MISMATCH":    "目标不匹配",
		"PRIM_MISSING":       "主文件缺失",
		"PRIM_ACCESS_FAIL":   "主文件访问失败",
		"SECO_MISSING":       "次文件缺失",
		"SECO_ACCESS_FAIL":   "次文件访问失败",
		"NOT_SAME_FILE":      "不是同一文件",
		"SRC_MISSING":        "源文件缺失",
		"DST_MISSING":        "目标文件缺失",
		"SRC_ACCESS_FAIL":    "源文件访问失败",
		"DST_ACCESS_FAIL":    "目标文件访问失败",
		"BOTH_MISSING":       "两者都缺失",
		"NOT_REGULAR_FILE":   "不是普通文件",
		"SIZE_MISMATCH":      "文件大小不一致",
		"CONTENT_MISMATCH":   "文件内容不一致",
	}
	usedTypes := make(map[string]bool)
	for _, r := range results {
		if r.ErrorType != "" {
			usedTypes[r.ErrorType] = true
		}
	}

	switch format {
	case JSON:
		sort.SliceStable(results, func(i, j int) bool {
			vi := resultMapValues(toSortedMap(results[i]))
			vj := resultMapValues(toSortedMap(results[j]))
			for idx := 0; idx < len(vi) && idx < len(vj); idx++ {
				if vi[idx] != vj[idx] {
					return vi[idx] < vj[idx]
				}
			}
			return len(vi) < len(vj)
		})
		sortedResults := make([]map[string]interface{}, len(results))
		for i, r := range results {
			sortedResults[i] = toSortedMap(r)
		}
		data, err := json.MarshalIndent(sortedResults, "", "    ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case Table:
		// 错误类型图例只在表格模式打印，避免污染 JSON 输出（此前无条件打印导致 --output json 前面掺入非 JSON 文本，无法被机器解析）
		if len(usedTypes) > 0 {
			fmt.Println("Error Types:")
			for et := range usedTypes {
				fmt.Printf("  %s: %s\n", et, errorTypes[et])
			}
			fmt.Println()
		}
		termWidth := pterm.GetTerminalWidth()
		colWidth := calcColWidth(termWidth)
		table := pterm.TableData{{"编号", "类型", "设备", "源路径", "链接路径", "有效", "错误类型"}}
		for i, r := range results {
			num := fmt.Sprintf("%d", i+1)
			valid := "是"
			if !r.Valid {
				valid = "否"
			}
			srcPath := truncateString(r.Real, colWidth)
			if srcPath == "" {
				srcPath = truncateString(r.Prim, colWidth)
			}
			if srcPath == "" {
				srcPath = truncateString(r.Src, colWidth)
			}
			linkPath := truncateString(r.Fake, colWidth)
			if linkPath == "" {
				linkPath = truncateString(r.Seco, colWidth)
			}
			if linkPath == "" {
				linkPath = truncateString(r.Dst, colWidth)
			}
			row := []string{num, truncateString(r.Type, 6), truncateString(r.Device, 8), srcPath, linkPath, valid, truncateString(r.ErrorType, 10)}
			if r.Valid {
				table = append(table, row)
			} else {
				table = append(table, []string{
					num,
					pterm.Red(truncateString(r.Type, 6)),
					pterm.Red(truncateString(r.Device, 8)),
					pterm.Red(srcPath),
					pterm.Red(linkPath),
					pterm.Red(valid),
					pterm.Red(truncateString(r.ErrorType, 10)),
				})
			}
		}
		pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(table).Render()
	}
	return nil
}

// PrintCheckResultsFix 打印 fix 命令的检查结果（table 模式带斑马条纹）
func PrintCheckResultsFix(format OutputFormat, results []CheckResult) error {
	if format != Table {
		return PrintCheckResults(format, results)
	}

	termWidth := pterm.GetTerminalWidth()
	colWidth := calcColWidth(termWidth)
	table := pterm.TableData{{"编号", "类型", "设备", "源路径", "链接路径", "有效", "错误类型"}}
	for i, r := range results {
		num := fmt.Sprintf("%d", i+1)
		valid := "是"
		if !r.Valid {
			valid = "否"
		}
		srcPath := truncateString(r.Real, colWidth)
		if srcPath == "" {
			srcPath = truncateString(r.Prim, colWidth)
		}
		if srcPath == "" {
			srcPath = truncateString(r.Src, colWidth)
		}
		linkPath := truncateString(r.Fake, colWidth)
		if linkPath == "" {
			linkPath = truncateString(r.Seco, colWidth)
		}
		if linkPath == "" {
			linkPath = truncateString(r.Dst, colWidth)
		}

		colorize := func(s string) string {
			if i%2 == 0 {
				return pterm.Red(s)
			}
			return pterm.LightMagenta(s)
		}

		row := []string{
			colorize(num),
			colorize(truncateString(r.Type, 6)),
			colorize(truncateString(r.Device, 8)),
			colorize(srcPath),
			colorize(linkPath),
			colorize(valid),
			colorize(truncateString(r.ErrorType, 10)),
		}
		table = append(table, row)
	}

	pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(table).Render()
	return nil
}

// calcColWidth 根据终端宽度计算表格中每列（源路径/链接路径）的可用宽度
// 公式与表格表头及固定列宽保持一致，并对结果做下限保护，避免终端过窄时算出负数导致截断越界 panic
func calcColWidth(termWidth int) int {
	// 表头: 编号(7) 类型(4) 设备(8) 源路径 链接路径 有效(4) 错误类型(10)，间隔约 3 字符共 6 处
	const fixed = 7 + 4 + 8 + 4 + 10 + 6*3
	w := (termWidth - fixed) / 3 - 3
	if w < 10 {
		// 终端过窄时给出最小可用宽度，防止 truncateString 传入负数而越界
		w = 10
	}
	return w
}

// truncateString 截断字符串，如果超过 maxLen
func truncateString(raw string, maxLen int) string {
	if maxLen < 3 {
		// 宽度不足以容纳省略号时直接原样返回，避免 runes[:maxLen-3] 越界
		return raw
	}
	runes := []rune(raw)
	if len(runes) <= maxLen {
		return raw
	}
	return string(runes[:maxLen-3]) + "..."
}

// PrintCreateResult 打印创建结果
func PrintCreateResult(format OutputFormat, result CreateResult) error {
	switch format {
	case JSON:
		data, err := json.MarshalIndent(toSortedMap(result), "", "    ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case Table:
		if result.Success && result.Message != "" {
			pterm.Success.Println(result.Type + ": " + result.Message)
		} else if !result.Success && result.Error != "" {
			pterm.Error.Println(result.Type + ": " + result.Error)
		}
	}
	return nil
}
