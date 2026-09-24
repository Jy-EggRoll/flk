package output

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/jy-eggroll/flk/pkg/l10n"
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

// PrintCheckResults 将检查结果按指定格式写入 writer
// JSON 模式只编码一个数组文档，表格模式则把错误类型图例和表格全部写入同一 writer，便于调用方重定向并感知写入失败
func PrintCheckResults(writer io.Writer, format OutputFormat, results []CheckResult) error {
	// 收集本次结果实际出现的错误类型，表格模式仅展示相关图例，避免无关说明占用终端空间
	errorTypes := map[string]string{
		"PATH_EXPAND_FAIL":     l10n.T("Path expansion failed", nil),
		"LINK_MISSING":         l10n.T("Link file missing", nil),
		"LINK_ACCESS_FAIL":     l10n.T("Link access failed", nil),
		"NOT_SYMLINK":          l10n.T("Not a symbolic link", nil),
		"READLINK_FAIL":        l10n.T("Failed to read link", nil),
		"TARGET_MISSING":       l10n.T("Target file missing", nil),
		"TARGET_ACCESS_FAIL":   l10n.T("Target access failed", nil),
		"EXPECTED_MISSING":     l10n.T("Expected file missing", nil),
		"EXPECTED_ACCESS_FAIL": l10n.T("Expected file access failed", nil),
		"TARGET_MISMATCH":      l10n.T("Target mismatch", nil),
		"PRIM_MISSING":         l10n.T("Primary file missing", nil),
		"PRIM_ACCESS_FAIL":     l10n.T("Primary file access failed", nil),
		"SECO_MISSING":         l10n.T("Secondary file missing", nil),
		"SECO_ACCESS_FAIL":     l10n.T("Secondary file access failed", nil),
		"NOT_SAME_FILE":        l10n.T("Not the same file", nil),
		"SRC_MISSING":          l10n.T("Source file missing", nil),
		"DST_MISSING":          l10n.T("Destination file missing", nil),
		"SRC_ACCESS_FAIL":      l10n.T("Source file access failed", nil),
		"DST_ACCESS_FAIL":      l10n.T("Destination file access failed", nil),
		"BOTH_MISSING":         l10n.T("Both files are missing", nil),
		"NOT_REGULAR_FILE":     l10n.T("Not a regular file", nil),
		"SIZE_MISMATCH":        l10n.T("File sizes differ", nil),
		"CONTENT_MISMATCH":     l10n.T("File contents differ", nil),
	}
	usedTypes := make(map[string]bool)
	for _, r := range results {
		if r.ErrorType != "" {
			usedTypes[r.ErrorType] = true
		}
	}

	switch format {
	case JSON:
		// 沿用既有的结果排序规则和字段 map，既保持数组顺序稳定，也保持 JSON 字段结构及字典序输出不变
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

		// Encoder 只调用一次，确保输出是一个完整 JSON 文档；非 nil 的空切片保证无结果时编码为 [] 而不是 null
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "    ")
		if err := encoder.Encode(sortedResults); err != nil {
			return err
		}
	case Table:
		// 错误类型图例只在表格模式打印，避免污染 JSON；map 键必须先排序，防止 Go 的随机遍历顺序造成输出抖动
		if len(usedTypes) > 0 {
			if _, err := fmt.Fprintln(writer, "Error Types:"); err != nil {
				return err
			}
			usedTypeNames := make([]string, 0, len(usedTypes))
			for errorType := range usedTypes {
				usedTypeNames = append(usedTypeNames, errorType)
			}
			sort.Strings(usedTypeNames)
			for _, errorType := range usedTypeNames {
				if _, err := fmt.Fprintf(writer, "  %s: %s\n", errorType, errorTypes[errorType]); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintln(writer); err != nil {
				return err
			}
		}
		termWidth := pterm.GetTerminalWidth()
		colWidth := calcColWidth(termWidth)
		table := pterm.TableData{{l10n.T("No.", nil), l10n.T("Type", nil), l10n.T("Device", nil), l10n.T("Source", nil), l10n.T("Link", nil), l10n.T("Valid", nil), l10n.T("Error", nil)}}
		for i, r := range results {
			num := fmt.Sprintf("%d", i+1)
			valid := l10n.T("Yes", nil)
			if !r.Valid {
				valid = l10n.T("No", nil)
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
		if err := writeTable(writer, table); err != nil {
			return err
		}
	}
	return nil
}

// PrintCheckResultsFix 将 fix 命令的检查结果写入 writer，表格模式保留既有的红色与浅紫色斑马条纹
// 非表格格式复用 PrintCheckResults，确保 JSON 的单文档结构、排序和写错误处理完全一致
func PrintCheckResultsFix(writer io.Writer, format OutputFormat, results []CheckResult) error {
	if format != Table {
		return PrintCheckResults(writer, format, results)
	}

	termWidth := pterm.GetTerminalWidth()
	colWidth := calcColWidth(termWidth)
	table := pterm.TableData{{l10n.T("No.", nil), l10n.T("Type", nil), l10n.T("Device", nil), l10n.T("Source", nil), l10n.T("Link", nil), l10n.T("Valid", nil), l10n.T("Error", nil)}}
	for i, r := range results {
		num := fmt.Sprintf("%d", i+1)
		valid := l10n.T("Yes", nil)
		if !r.Valid {
			valid = l10n.T("No", nil)
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

	return writeTable(writer, table)
}

// writeTable 使用 pterm 生成与既有终端表格一致的文本，再显式写入调用方提供的 writer
// pterm.TablePrinter.Render 会忽略底层写错误，因此这里必须调用 Srender 并自行写入，才能把磁盘满、管道关闭等错误原样返回
func writeTable(writer io.Writer, data pterm.TableData) error {
	rendered, err := pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(data).Srender()
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, rendered+"\n")
	return err
}

// calcColWidth 根据终端宽度计算表格中每列（源路径/链接路径）的可用宽度
// 公式与表格表头及固定列宽保持一致，并对结果做下限保护，避免终端过窄时算出负数导致截断越界 panic
func calcColWidth(termWidth int) int {
	// 表头: 编号(7) 类型(4) 设备(8) 源路径 链接路径 有效(4) 错误类型(10)，间隔约 3 字符共 6 处
	const fixed = 7 + 4 + 8 + 4 + 10 + 6*3
	w := (termWidth-fixed)/3 - 3
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

// PrintCreateResult 将创建结果按指定格式写入 writer
// JSON 模式保持既有对象结构和四空格缩进，人类可读模式保留 pterm 的成功、失败前缀及原有文案
func PrintCreateResult(writer io.Writer, format OutputFormat, result CreateResult) error {
	switch format {
	case JSON:
		// 使用一次 Encode 输出一个完整对象文档，并保留 toSortedMap 提供的字段命名、omitempty 与稳定键序
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "    ")
		if err := encoder.Encode(toSortedMap(result)); err != nil {
			return err
		}
	case Table:
		var text string
		if result.Success && result.Message != "" {
			text = pterm.Success.Sprintln(result.Type + ": " + result.Message)
		} else if !result.Success && result.Error != "" {
			text = pterm.Error.Sprintln(result.Type + ": " + result.Error)
		}
		if text != "" {
			// 不调用 PrefixPrinter.Println，因为 pterm 会吞掉底层 writer 错误，显式写入才能兑现本函数的 error 返回值
			if _, err := io.WriteString(writer, text); err != nil {
				return err
			}
		}
	}
	return nil
}
