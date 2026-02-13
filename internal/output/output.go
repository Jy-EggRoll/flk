package output

import (
	"encoding/json"
	"fmt"

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
	Path      string `json:"path"`
	BasePath  string `json:"base_path,omitempty"`
	Real      string `json:"real,omitempty"`
	Fake      string `json:"fake,omitempty"`
	Prim      string `json:"prim,omitempty"`
	Seco      string `json:"seco,omitempty"`
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

// PrintCheckResults 打印检查结果
func PrintCheckResults(format OutputFormat, results []CheckResult) error {
	// 收集错误类型并打印解释
	errorTypes := map[string]string{
		"PATH_EXPAND_FAIL":     "路径展开失败",
		"LINK_MISSING":         "链接文件缺失",
		"LINK_ACCESS_FAIL":     "链接访问失败",
		"NOT_SYMLINK":          "不是符号链接",
		"READLINK_FAIL":        "读取链接失败",
		"TARGET_MISSING":       "目标文件缺失",
		"TARGET_ACCESS_FAIL":   "目标访问失败",
		"EXPECTED_MISSING":     "期望文件缺失",
		"EXPECTED_ACCESS_FAIL": "期望访问失败",
		"TARGET_MISMATCH":      "目标不匹配",
		"PRIM_MISSING":         "主文件缺失",
		"PRIM_ACCESS_FAIL":     "主文件访问失败",
		"SECO_MISSING":         "次文件缺失",
		"SECO_ACCESS_FAIL":     "次文件访问失败",
		"NOT_SAME_FILE":        "不是同一文件",
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
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case Table:
		// 动态调整列宽，截断长路径
		termWidth := pterm.GetTerminalWidth()
		table := pterm.TableData{{"编号", "类型", "设备", "父路径", "相对路径", "绝对路径", "有效", "错误类型"}}
		for i, r := range results {
			num := fmt.Sprintf("%d", i+1)
			valid := "是"
			if !r.Valid {
				valid = "否"
			}
			relPath := truncateString(r.Real, (termWidth-7*3-4-8-4-10)/3-3)
			if relPath == "" {
				relPath = truncateString(r.Prim, (termWidth-7*3-4-8-4-10)/3-3)
			}
			absPath := truncateString(r.Fake, (termWidth-7*3-4-8-4-10)/3-3)
			if absPath == "" {
				absPath = truncateString(r.Seco, (termWidth-7*3-4-8-4-10)/3-3)
			}
			row := []string{num, truncateString(r.Type, 6), truncateString(r.Device, 8), truncateString(r.Path, (termWidth-7*3-4-8-4-10)/3-3), relPath, absPath, valid, truncateString(r.ErrorType, 10)}
			if r.Valid {
				table = append(table, row)
			} else {
				table = append(table, []string{
					num,
					pterm.Red(truncateString(r.Type, 6)),
					pterm.Red(truncateString(r.Device, 8)),
					pterm.Red(truncateString(r.Path, (termWidth-7*3-4-8-4-10)/3-3)),
					pterm.Red(relPath),
					pterm.Red(absPath),
					pterm.Red(valid),
					pterm.Red(truncateString(r.ErrorType, 10)),
				})
			}
		}
		pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(table).Render()
	}
	return nil
}

// truncateString 截断路径，如果超过 maxLen
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
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	case Table:
		table := pterm.TableData{{"成功", "类型", "消息", "错误"}}
		success := "是"
		if !result.Success {
			success = "否"
		}
		table = append(table, []string{success, result.Type, result.Message, result.Error})
		pterm.DefaultTable.WithHasHeader().WithData(table).Render()
	}
	return nil
}
