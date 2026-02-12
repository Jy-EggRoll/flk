// Package interact 提供命令行交互功能
// 主要功能包括：
// 1. 询问用户是/否问题
// 2. 显示选择菜单
// 3. 格式化输出信息
package interact

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
)

// AskYesNo 询问用户是/否问题
// question: 要询问的问题
// defaultYes: 默认答案是否为"是"
// 返回: true 表示用户选择"是"，false 表示"否"
func AskYesNo(question string, defaultYes bool) bool {
	// 构造提示符
	prompt := question
	if defaultYes {
		prompt += "【Y/n】"
	} else {
		prompt += "【y/N】"
	}

	fmt.Print(prompt)

	// 读取用户输入
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		// 读取失败，返回默认值
		return defaultYes
	}

	// 清理输入（去除空白字符）
	input = strings.TrimSpace(input)
	input = strings.ToLower(input)

	// 如果用户直接按回车，返回默认值
	if input == "" {
		return defaultYes
	}

	// 判断用户输入
	// 接受: y, yes, 是
	// 拒绝: n, no, 否
	switch input {
	case "y", "yes", "是":
		return true
	case "n", "no", "否":
		return false
	default:
		// 无法识别的输入，返回默认值
		fmt.Printf("无法识别的输入，使用默认值“%v”\n", defaultYes)
		return defaultYes
	}
}

// AskChoice 让用户从多个选项中选择一个
// question: 问题描述
// choices: 选项列表，每个元素是一个包含描述和对应字母的字符串，例如 "重新创建符号链接 (r)"
// 返回: 用户选择的字母的小写形式，如果无效则返回空字符串
func AskChoice(question string, choices map[string]string) string {
	fmt.Println(question)

	// 显示所有选项
	for key, desc := range choices {
		fmt.Printf("  %s: %s\n", key, desc)
	}
	fmt.Print("请输入对应字母：")

	// 读取用户输入
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}

	// 清理输入并转换为小写
	input = strings.TrimSpace(strings.ToLower(input))

	// 检查输入是否有效
	if _, ok := choices[input]; ok {
		return input
	}

	fmt.Println("无效的输入")
	return ""
}

// PrintSuccess 打印成功消息（绿色）
func PrintSuccess(format string, args ...any) {
	color.Green(format, args...)
}

// PrintError 打印错误消息（红色）
func PrintError(format string, args ...any) {
	color.Red(format, args...)
}

// PrintWarning 打印警告消息（黄色）
func PrintWarning(format string, args ...any) {
	color.Yellow(format, args...)
}

// PrintInfo 打印信息消息（蓝色）
func PrintInfo(format string, args ...any) {
	color.Blue(format, args...)
}

// ReadLine 读取一行用户输入
func ReadLine(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}
