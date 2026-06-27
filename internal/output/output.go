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
		"BOTH_MISSING":       "两者都缺失",
		"MOD_TIME_MISMATCH":  "修改时间不一致",
	}
	usedTypes := make(map[string]bool)
	for _, r := range results {
		if r.ErrorType != "" {
			usedTypes[r.ErrorType] = true
		}
	}
	if len(usedTypes) > 0 {
		fmt.Println("Error Types:")
		for et := range usedTypes {
			fmt.Printf("  %s: %s\n", et, errorTypes[et])
		}
		fmt.Println()
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
		termWidth := pterm.GetTerminalWidth()
		table := pterm.TableData{{"编号", "类型", "设备", "源路径", "链接路径", "有效", "错误类型"}}
		for i, r := range results {
			num := fmt.Sprintf("%d", i+1)
			valid := "是"
			if !r.Valid {
				valid = "否"
			}
			srcPath := truncateString(r.Real, (termWidth-7*3-4-8-4-10)/3-3)
			if srcPath == "" {
				srcPath = truncateString(r.Prim, (termWidth-7*3-4-8-4-10)/3-3)
			}
			if srcPath == "" {
				srcPath = truncateString(r.Src, (termWidth-7*3-4-8-4-10)/3-3)
			}
			linkPath := truncateString(r.Fake, (termWidth-7*3-4-8-4-10)/3-3)
			if linkPath == "" {
				linkPath = truncateString(r.Seco, (termWidth-7*3-4-8-4-10)/3-3)
			}
			if linkPath == "" {
				linkPath = truncateString(r.Dst, (termWidth-7*3-4-8-4-10)/3-3)
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
	table := pterm.TableData{{"编号", "类型", "设备", "源路径", "链接路径", "有效", "错误类型"}}
	for i, r := range results {
		num := fmt.Sprintf("%d", i+1)
		valid := "是"
		if !r.Valid {
			valid = "否"
		}
		srcPath := truncateString(r.Real, (termWidth-7*3-4-8-4-10)/3-3)
		if srcPath == "" {
			srcPath = truncateString(r.Prim, (termWidth-7*3-4-8-4-10)/3-3)
		}
		if srcPath == "" {
			srcPath = truncateString(r.Src, (termWidth-7*3-4-8-4-10)/3-3)
		}
		linkPath := truncateString(r.Fake, (termWidth-7*3-4-8-4-10)/3-3)
		if linkPath == "" {
			linkPath = truncateString(r.Seco, (termWidth-7*3-4-8-4-10)/3-3)
		}
		if linkPath == "" {
			linkPath = truncateString(r.Dst, (termWidth-7*3-4-8-4-10)/3-3)
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

// truncateString 截断字符串，如果超过 maxLen
func truncateString(raw string, maxLen int) string {
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
