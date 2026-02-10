package store // 声明当前代码所属的包名为 store，用于封装存储相关的核心逻辑

import ( // 导入代码依赖的外部包，采用分组导入的方式提升代码整洁性

	"encoding/json"
	"fmt"
	"os"
	"os/user" // 导入 os/user 包，用于获取当前操作系统用户的信息（如用户主目录路径）
	"path/filepath"
	"runtime" // 导入 runtime 包，用于获取程序运行时的环境信息（如操作系统类型）
	"strings" // 导入 strings 包，用于执行字符串的各类操作（如前缀判断、子串替换）

	"github.com/jy-eggroll/flk/internal/logger"
)

// BaseEntry 用于承载通用的 JSON 序列化逻辑
type Entry map[string]string           // 定义 Entry 类型，底层为键值对映射结构，作为基础数据单元承载可 JSON 序列化的通用数据
type PathGroup map[string][]Entry      // 定义 PathGroup 类型，按路径字符串为键，存储对应路径下的多个 Entry 实例切片
type TypeGroup map[string]PathGroup    // 定义 TypeGroup 类型，按链接类型字符串为键，存储对应类型下的多个 PathGroup 实例
type DeviceGroup map[string]TypeGroup  // 定义 DeviceGroup 类型，按设备标识字符串为键，存储对应设备下的多个 TypeGroup 实例
type RootConfig map[string]DeviceGroup // 定义 RootConfig 类型，按操作系统平台字符串为键，存储对应平台下的多个 DeviceGroup 实例

type Manager struct { // 定义 Manager 结构体，作为存储数据的核心管理对象
	Data RootConfig // Manager 的核心数据字段，存储按平台-设备-类型-路径层级组织的所有 Entry 数据
}

func foldPath(path string) string { // 定义 foldPath 函数，接收原始路径字符串，返回将用户主目录替换为~的简化路径
	u, _ := user.Current()             // 获取当前系统用户信息，忽略返回的错误（简化场景下的处理方式），赋值给变量 u
	home := u.HomeDir                  // 从当前用户信息中提取用户主目录的绝对路径，赋值给变量 home
	if strings.HasPrefix(path, home) { // 判断传入的原始路径是否以用户主目录路径为前缀
		return strings.Replace(path, home, "~", 1) // 若路径包含主目录前缀，将第一个主目录子串替换为~后返回
	}
	return path // 若路径不包含主目录前缀，直接返回原始路径字符串
}

func (m *Manager) AddRecord(device, linkType, parentPath string, fields map[string]string) { // 定义 Manager 的 AddRecord 方法，用于添加一条存储记录，参数依次为设备标识、链接类型、父路径、字段键值对
	// 如果未指定设备，默认使用 all，确保聚合在同一分组下
	if device == "" {
		device = "all"
	}
	platform := runtime.GOOS // 获取当前程序运行的操作系统平台标识（如 linux/darwin/windows），赋值给变量 platform

	// 初始化层级（防御性编程）
	if m.Data[platform] == nil { // 检查当前平台对应的 DeviceGroup 是否未初始化（nil）
		m.Data[platform] = make(DeviceGroup) // 初始化 DeviceGroup 类型的映射，避免后续操作出现空指针异常
	}
	if m.Data[platform][device] == nil { // 检查当前设备对应的 TypeGroup 是否未初始化（nil）
		m.Data[platform][device] = make(TypeGroup) // 初始化 TypeGroup 类型的映射，保证层级数据结构的完整性
	}

	foldedParent := foldPath(parentPath)           // 调用 foldPath 函数处理父路径，将其中的用户主目录替换为~符号
	if m.Data[platform][device][linkType] == nil { // 检查当前链接类型对应的 PathGroup 是否未初始化（nil）
		m.Data[platform][device][linkType] = make(PathGroup) // 初始化 PathGroup 类型的映射，确保路径层级可正常存储数据
	}

	// 处理内部字段的路径折叠
	processedEntry := make(Entry) // 初始化 Entry 类型的映射，用于存储处理后的字段键值对
	for k, v := range fields {    // 遍历传入的原始字段键值对，k 为字段名，v 为字段原始值
		processedEntry[k] = foldPath(v) // 对每个字段值执行路径简化处理，将结果存入 processedEntry
	}

	m.Data[platform][device][linkType][foldedParent] = append( // 调用 append 函数，将处理后的 Entry 添加到对应层级的切片中
		m.Data[platform][device][linkType][foldedParent], // 目标切片：当前平台-设备-类型-简化路径对应的 Entry 切片
		processedEntry, // 待追加的元素：处理完成的 Entry 实例
	)

	logger.Debug("结构创建成功")
	fmt.Print(m.ToJSON())
}

func (m *Manager) ToJSON() string {
	jsonResult, _ := json.MarshalIndent(m.Data, "", "    ")
	return string(jsonResult)
}

// DefaultStorePath 指定默认的持久化存储路径（不展开 JSON 中的 ~，由写入时展开实际文件系统路径）
const DefaultStorePath = "~/.config/flk/flk-store.json"

// StorePath 用于 Cobra 参数绑定，默认值为 DefaultStorePath
var StorePath = DefaultStorePath

// GlobalManager 是全局共享的 Manager 实例，用于在启动阶段加载现有数据并在命令之间共享状态
var GlobalManager *Manager

// InitStore 初始化全局存储，若目标文件存在则加载，否则创建一个空的存储结构
func InitStore(storePath string) error {
	// 尝试从文件加载
	m, err := LoadFromFile(storePath)
	if err != nil {
		// 如果文件不存在，初始化空数据结构
		if os.IsNotExist(err) {
			m = &Manager{Data: make(RootConfig)}
		} else {
			return err
		}
	}
	GlobalManager = m
	return nil
}

// expandStorePath 将 ~ 展开为用户主目录，用于文件系统操作。
func expandStorePath(p string) (string, error) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
			return filepath.Join(home, p[2:]), nil
		}
	}
	return p, nil
}

// Save 将当前 Manager 的数据持久化到指定文件路径（保留 ~，不在 JSON 中展开）。
func (m *Manager) Save(filePath string) error {
	data, err := json.MarshalIndent(m.Data, "", "    ")
	if err != nil {
		return err
	}
	expanded, err := expandStorePath(filePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(expanded), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(expanded, data, 0644); err != nil {
		return err
	}
	return nil
}

// LoadFromFile 从指定路径加载并返回一个 Manager 实例（路径中可包含 ~，会被展开）。
func LoadFromFile(filePath string) (*Manager, error) {
	expanded, err := expandStorePath(filePath)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(expanded)
	if err != nil {
		return nil, err
	}
	var data RootConfig
	if len(b) > 0 {
		if err := json.Unmarshal(b, &data); err != nil {
			return nil, err
		}
	} else {
		data = make(RootConfig)
	}
	return &Manager{Data: data}, nil
}
