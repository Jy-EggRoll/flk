package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// errTestWrite 是测试 writer 固定返回的错误，用于确认公开输出函数不会吞掉底层写失败
var errTestWrite = errors.New("测试写入失败")

// failingWriter 模拟磁盘满或管道关闭：任何写入都立即失败，且不接收部分数据
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errTestWrite
}

// TestPrintCheckResultsJSONSingleDocument 验证检查结果仍是单个 JSON 数组文档，并保持既有稳定排序与字段结构
func TestPrintCheckResultsJSONSingleDocument(t *testing.T) {
	results := []CheckResult{
		{Type: "copy", Device: "z-device", Src: "/src", Dst: "/dst", Valid: true},
		{Type: "copy", Device: "a-device", Src: "/src", Dst: "/dst", Valid: true},
	}

	var buffer bytes.Buffer
	if err := PrintCheckResults(&buffer, JSON, results); err != nil {
		t.Fatalf("输出 JSON 失败: %v", err)
	}

	decoder := json.NewDecoder(&buffer)
	var decoded []map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("解析检查结果 JSON 失败: %v", err)
	}
	if err := assertJSONDocumentEnd(decoder); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 {
		t.Fatalf("结果数量 = %d，期望 2", len(decoded))
	}
	if decoded[0]["device"] != "a-device" || decoded[1]["device"] != "z-device" {
		t.Fatalf("结果顺序不稳定: %#v", decoded)
	}
	if decoded[0]["valid"] != true {
		t.Fatalf("valid 字段结构异常: %#v", decoded[0])
	}
	if _, exists := decoded[0]["error"]; exists {
		t.Fatalf("空 error 应继续遵循 omitempty: %#v", decoded[0])
	}
}

// TestPrintCheckResultsJSONEmptyArray 确保 nil 结果也编码为空数组，避免机器调用方收到语义不同的 null
func TestPrintCheckResultsJSONEmptyArray(t *testing.T) {
	var buffer bytes.Buffer
	if err := PrintCheckResults(&buffer, JSON, nil); err != nil {
		t.Fatalf("输出空 JSON 失败: %v", err)
	}
	if got := buffer.String(); got != "[]\n" {
		t.Fatalf("空结果输出 = %q，期望 %q", got, "[]\n")
	}
}

// TestPrintCheckResultsFixJSONSingleDocument 验证 fix 的 JSON 分支复用相同 writer，且不会在 JSON 前后混入表格文本
func TestPrintCheckResultsFixJSONSingleDocument(t *testing.T) {
	var buffer bytes.Buffer
	if err := PrintCheckResultsFix(&buffer, JSON, []CheckResult{{Type: "symlink", Device: "dev", Valid: false}}); err != nil {
		t.Fatalf("输出 fix JSON 失败: %v", err)
	}

	decoder := json.NewDecoder(&buffer)
	var decoded []CheckResult
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("解析 fix JSON 失败: %v", err)
	}
	if err := assertJSONDocumentEnd(decoder); err != nil {
		t.Fatal(err)
	}
}

// TestPrintCreateResultJSONSingleDocument 验证创建结果保持对象结构、omitempty 语义并且只编码一个文档
func TestPrintCreateResultJSONSingleDocument(t *testing.T) {
	var buffer bytes.Buffer
	if err := PrintCreateResult(&buffer, JSON, CreateResult{Success: true, Type: "copy", Message: "创建成功"}); err != nil {
		t.Fatalf("输出创建结果 JSON 失败: %v", err)
	}

	decoder := json.NewDecoder(&buffer)
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("解析创建结果 JSON 失败: %v", err)
	}
	if err := assertJSONDocumentEnd(decoder); err != nil {
		t.Fatal(err)
	}
	if decoded["success"] != true || decoded["type"] != "copy" || decoded["message"] != "创建成功" {
		t.Fatalf("创建结果结构异常: %#v", decoded)
	}
	if _, exists := decoded["error"]; exists {
		t.Fatalf("空 error 应继续遵循 omitempty: %#v", decoded)
	}
}

// TestPrintCheckResultsTableStableErrorTypes 验证图例按错误类型代码排序，而不是依赖 map 的随机遍历顺序
func TestPrintCheckResultsTableStableErrorTypes(t *testing.T) {
	results := []CheckResult{
		{Type: "copy", Device: "dev", Valid: false, ErrorType: "TARGET_MISSING"},
		{Type: "copy", Device: "dev", Valid: false, ErrorType: "BOTH_MISSING"},
		{Type: "copy", Device: "dev", Valid: false, ErrorType: "CONTENT_MISMATCH"},
	}

	var first string
	for attempt := 0; attempt < 20; attempt++ {
		var buffer bytes.Buffer
		if err := PrintCheckResults(&buffer, Table, results); err != nil {
			t.Fatalf("第 %d 次输出表格失败: %v", attempt+1, err)
		}
		got := buffer.String()
		if first == "" {
			first = got
		} else if got != first {
			t.Fatalf("相同输入的表格输出不稳定\n首次:\n%s\n本次:\n%s", first, got)
		}

		both := strings.Index(got, "  BOTH_MISSING:")
		content := strings.Index(got, "  CONTENT_MISMATCH:")
		target := strings.Index(got, "  TARGET_MISSING:")
		if both < 0 || content < 0 || target < 0 || !(both < content && content < target) {
			t.Fatalf("Error Types 未按代码排序:\n%s", got)
		}
	}
}

// TestOutputFunctionsPropagateWriterErrors 覆盖 JSON、图例、表格和人类文案路径，确保三个公开函数都传播 writer 错误
func TestOutputFunctionsPropagateWriterErrors(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "check JSON", run: func() error { return PrintCheckResults(failingWriter{}, JSON, nil) }},
		{name: "check table legend", run: func() error {
			return PrintCheckResults(failingWriter{}, Table, []CheckResult{{ErrorType: "BOTH_MISSING"}})
		}},
		{name: "fix table", run: func() error {
			return PrintCheckResultsFix(failingWriter{}, Table, []CheckResult{{Type: "copy"}})
		}},
		{name: "create JSON", run: func() error {
			return PrintCreateResult(failingWriter{}, JSON, CreateResult{Success: true, Type: "copy"})
		}},
		{name: "create human text", run: func() error {
			return PrintCreateResult(failingWriter{}, Table, CreateResult{Success: true, Type: "copy", Message: "成功"})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, errTestWrite) {
				t.Fatalf("返回错误 = %v，期望 %v", err, errTestWrite)
			}
		})
	}
}

// assertJSONDocumentEnd 要求首次 Decode 后只能到达 EOF，从而识别连续输出多个 JSON 文档或夹杂额外文本的问题
func assertJSONDocumentEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("输出包含一个 JSON 文档之外的额外内容")
	}
	return nil
}
