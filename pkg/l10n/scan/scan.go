// Package scan 从 Go 源码里提取 l10n 消息。
//
// 因为采用"英文源串即消息 id"的模型（见 pkg/l10n 的包注释），提取规则极其简单：
// 消息函数的第一个参数就是一条消息。不存在"某个字面量到底算不算文案"的启发式判断，
// 也因此不需要标记注释或白名单——这正是该模型相对"符号 key"方案的核心优势。
//
// 识别方式只有一种：**导入路径解析到 Config.ImportPath 的那个别名上的 .T(...) 调用**。
// 刻意不支持"在调用方再包一层转发函数"：那会让消息 id 变成运行期的变量，
// 提取器无法静态读出，规则一旦开口子就会到处漏水。
package scan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jy-eggroll/flk/pkg/l10n"
)

// Config 描述一次扫描的范围。
type Config struct {
	// Root 是仓库根（相对路径的基准）。
	Root string
	// ImportPath 是消息函数所在包的导入路径，如 "github.com/jy-eggroll/flk/pkg/l10n"。
	ImportPath string
	// SrcDirs 是参与扫描的顶层目录（相对 Root）。目录不存在会被跳过。
	SrcDirs []string
}

// Literal 记录一处字符串字面量及其源码位置。
type Literal struct {
	Text string
	Pos  token.Position
}

// Result 汇总一次源码扫描的结果。
type Result struct {
	// Messages 是被消息函数引用的消息，按文本去重、按文本排序
	Messages []Literal
	// Unwrapped 是"含非 ASCII 字母但没被消息函数包住"的字面量，即尚未迁移的存量文案。
	// 它是迁移进度的度量：迁移完成后应当为空
	Unwrapped []Literal
	// Files 是实际解析的文件数，供调用方识别"一个文件都没扫到"这种门禁静默失效
	Files int
}

// Scan 扫描 cfg 指定的源码范围，提取消息与待迁移文案。
func Scan(cfg Config) (*Result, error) {
	if cfg.ImportPath == "" {
		return nil, fmt.Errorf("scan: Config.ImportPath must not be empty")
	}

	paths, err := goFiles(cfg.Root, cfg.SrcDirs)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	type parsedFile struct {
		path string
		file *ast.File
	}
	files := make([]parsedFile, 0, len(paths))
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", rel(cfg.Root, p), err)
		}
		files = append(files, parsedFile{p, f})
	}

	res := &Result{Files: len(files)}
	seen := map[string]bool{}
	wrapped := map[token.Pos]bool{}

	for _, pf := range files {
		// 只有导入了消息包的文件才可能有消息调用
		alias := importAlias(pf.file, cfg.ImportPath)
		if alias == "" {
			collectUnwrapped(fset, pf.file, wrapped, res)
			continue
		}
		if err := collectMessages(cfg.Root, fset, pf.file, alias, res, seen, wrapped); err != nil {
			return nil, err
		}
		collectUnwrapped(fset, pf.file, wrapped, res)
	}

	sort.Slice(res.Messages, func(i, j int) bool { return res.Messages[i].Text < res.Messages[j].Text })
	sort.Slice(res.Unwrapped, func(i, j int) bool {
		a, b := res.Unwrapped[i].Pos, res.Unwrapped[j].Pos
		if a.Filename != b.Filename {
			return a.Filename < b.Filename
		}
		return a.Line < b.Line
	})

	for i := range res.Messages {
		res.Messages[i].Pos.Filename = rel(cfg.Root, res.Messages[i].Pos.Filename)
	}
	for i := range res.Unwrapped {
		res.Unwrapped[i].Pos.Filename = rel(cfg.Root, res.Unwrapped[i].Pos.Filename)
	}

	return res, nil
}

// collectMessages 遍历单个文件，把所有消息调用的首参登记为消息。
func collectMessages(root string, fset *token.FileSet, file *ast.File, alias string,
	res *Result, seen map[string]bool, wrapped map[token.Pos]bool) error {

	var scanErr error
	ast.Inspect(file, func(n ast.Node) bool {
		if scanErr != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok || !isMessageCall(call, alias) {
			return true
		}

		const hint = "消息函数的首个参数必须是字符串字面量——消息 id 即英文原文，不能是变量、常量或拼接结果"
		if len(call.Args) == 0 {
			scanErr = fmt.Errorf("%s: 消息调用缺少参数", position(root, fset, call.Pos()))
			return false
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			scanErr = fmt.Errorf("%s: %s", position(root, fset, call.Args[0].Pos()), hint)
			return false
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			scanErr = fmt.Errorf("%s: 解析字符串字面量失败: %w", position(root, fset, lit.Pos()), err)
			return false
		}
		if err := validateMessage(text, position(root, fset, lit.Pos())); err != nil {
			scanErr = err
			return false
		}
		wrapped[lit.Pos()] = true
		if !seen[text] {
			seen[text] = true
			res.Messages = append(res.Messages, Literal{Text: text, Pos: fset.Position(lit.Pos())})
		}
		return true
	})
	return scanErr
}

// collectUnwrapped 收集没有被消息函数包住的非 ASCII 字面量，即尚未迁移的存量文案。
func collectUnwrapped(fset *token.FileSet, file *ast.File, wrapped map[token.Pos]bool, res *Result) {
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || wrapped[lit.Pos()] {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil || !hasNonASCII(text) {
			return true
		}
		res.Unwrapped = append(res.Unwrapped, Literal{Text: text, Pos: fset.Position(lit.Pos())})
		return true
	})
}

// isMessageCall 判断一次调用是否是消息调用：形如 <别名>.T(...) 的选择器调用。
//
// 只认这一种形态。裸 T(...) 不算——否则别的包里的同名函数或类型参数（如泛型函数
// 的类型参数 T）会被误判成消息调用，而那种误判会随无关重构随时冒出来。
func isMessageCall(call *ast.CallExpr, alias string) bool {
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || fun.Sel.Name != "T" || alias == "" {
		return false
	}
	x, ok := fun.X.(*ast.Ident)
	return ok && x.Name == alias
}

// importAlias 返回本文件引用 importPath 时使用的本地名；未导入返回空串。
func importAlias(file *ast.File, importPath string) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		if imp.Name != nil {
			// 点导入与空导入不会以选择器形式调用，视为没有可用别名
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		// 未显式起别名时，本地名是导入路径的最后一段
		return baseName(importPath)
	}
	return ""
}

// baseName 取导入路径的最后一段，即未起别名时的包名，如 "github.com/jy-eggroll/flk/pkg/l10n" -> "l10n"。
func baseName(importPath string) string {
	if i := strings.LastIndexByte(importPath, '/'); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}

// validateMessage 校验一条消息能否安全地用作 id。
//
// 除保留字（会让整份语言文件解析失败）外，还强制多行原文保持"干净"：
// id 就是原文本身，源码里任何缩进或行尾空白的变化都会让 id 漂移，
// 使既有译文静默失效。把这条约束前移到提取阶段，比事后排查便宜得多。
func validateMessage(text string, pos string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s: 消息不能是空串或纯空白", pos)
	}
	if err := l10n.ValidateMessageID(text); err != nil {
		return fmt.Errorf("%s: %w", pos, err)
	}
	if strings.Contains(text, "\t") {
		return fmt.Errorf("%s: 消息含制表符，id 会随源码缩进变化，请改用空格", pos)
	}
	if strings.TrimSpace(text) != text {
		return fmt.Errorf("%s: 消息首尾有多余空白", pos)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimRight(line, " ") != line {
			return fmt.Errorf("%s: 消息存在行尾空白", pos)
		}
	}
	return nil
}

// hasNonASCII 判断字符串是否含非 ASCII **字母**。
//
// 判据刻意用 unicode.IsLetter 而不是"非 ASCII 字符"：消息 id 一律是英文，所以
// 非 ASCII 字母就是还没迁移的存量文案；而排版符号——分隔线 "─"、箭头 "→"、"×"
// 等——是界面骨架而非文字，既不该翻译也不该被列为待办。
func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// goFiles 列出参与扫描的 Go 文件（跳过测试文件）。
// 扫描目录不存在时直接跳过：仓库未必同时拥有全部目录，缺失不是错误；
// 但"一个文件都没扫到"会被调用方当成错误拦下，避免门禁静默失效。
func goFiles(root string, dirs []string) ([]string, error) {
	var out []string
	for _, dir := range dirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			out = append(out, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// position 返回相对仓库根的源码位置，便于在报告中直接点击跳转。
func position(root string, fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return fmt.Sprintf("%s:%d", rel(root, p.Filename), p.Line)
}

// rel 把绝对路径转成相对仓库根的路径，失败时原样返回。
func rel(root, path string) string {
	if path == "" {
		return path
	}
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return r
}
