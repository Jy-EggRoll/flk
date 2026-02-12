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
	Plain OutputFormat = "plain"
)

// CheckResult 单个链接的检查结果
type CheckResult struct {
	Type      string `json:"type"`
	Device    string `json:"device"`
	Path      string `json:"path"`
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
		table := pterm.TableData{{"类型", "设备", "路径", "相对路径", "绝对路径", "有效", "错误类型"}}
		for _, r := range results {
			valid := "是"
			if !r.Valid {
				valid = "否"
			}
			relPath := truncatePath(r.Real, 20)
			if relPath == "" {
				relPath = truncatePath(r.Prim, 20)
			}
			absPath := truncatePath(r.Fake, 30)
			if absPath == "" {
				absPath = truncatePath(r.Seco, 30)
			}
			row := []string{r.Type, r.Device, truncatePath(r.Path, 20), relPath, absPath, valid, r.ErrorType}
			if r.Valid {
				table = append(table, row)
			} else {
				table = append(table, []string{
					pterm.Red(r.Type),
					pterm.Red(r.Device),
					pterm.Red(truncatePath(r.Path, 20)),
					pterm.Red(relPath),
					pterm.Red(absPath),
					pterm.Red(valid),
					pterm.Red(r.ErrorType),
				})
			}
		}
		pterm.DefaultTable.WithHasHeader().WithBoxed(false).WithData(table).Render() // 无框，紧凑
	}
	return nil
}

// truncatePath 截断路径，如果超过 maxLen（UTF-8 安全）
func truncatePath(path string, maxLen int) string {
	runes := []rune(path)
	if len(runes) <= maxLen {
		return path
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
	case Plain:
		if result.Success {
			fmt.Printf("%s 创建成功\n", result.Type)
			if result.Message != "" {
				fmt.Printf("消息: %s\n", result.Message)
			}
		} else {
			fmt.Printf("%s 创建失败\n", result.Type)
			if result.Error != "" {
				fmt.Printf("错误: %s\n", result.Error)
			}
		}
	}
	return nil
}
