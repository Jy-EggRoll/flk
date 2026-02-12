package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"file-link-manager/internal/interact"
	"file-link-manager/internal/linkop"
	"file-link-manager/internal/location"
	"file-link-manager/internal/logger"
	"file-link-manager/internal/pathutil"
	"file-link-manager/internal/storage"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "创建符号链接或硬链接",
	Long: `创建符号链接或硬链接

支持两种子命令：
  symlink - 创建符号链接（软链接）
  hardlink - 创建硬链接`,
}

var createSymlinkCmd = &cobra.Command{
	Use:   "symlink",
	Short: "创建符号链接",
	Long: `创建符号链接（软链接）

符号链接指向另一个文件或目录，类似快捷方式
使用“--real”指定真实文件路径，“--fake”指定链接路径`,
	RunE: runCreateSymlink,
}

var createHardlinkCmd = &cobra.Command{
	Use:   "hardlink",
	Short: "创建硬链接",
	Long: `创建硬链接

硬链接是文件的另一个名称，与原文件指向同一个inode
使用“--prim”指定主要文件路径，“--seco”指定次要文件路径`,
	RunE: runCreateHardlink,
}

var (
	// 符号链接参数
	symlinkReal string
	symlinkFake string

	// 硬链接参数
	hardlinkPrim string
	hardlinkSeco string

	// 共用参数
	createForce  bool
	createDevice string // 设备名称，用于设备过滤
)

func init() {
	rootCmd.AddCommand(createCmd)
	createCmd.AddCommand(createSymlinkCmd)
	createCmd.AddCommand(createHardlinkCmd)

	// 符号链接标志
	createSymlinkCmd.Flags().StringVarP(&symlinkReal, "real", "r", "", "真实文件路径")
	createSymlinkCmd.Flags().StringVarP(&symlinkFake, "fake", "f", "", "链接文件路径")
	createSymlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件")
	createSymlinkCmd.Flags().StringVar(&createDevice, "device", "", "设备名称，用于后续设备过滤检查")
	createSymlinkCmd.MarkFlagRequired("real")
	createSymlinkCmd.MarkFlagRequired("fake")

	// 硬链接标志
	createHardlinkCmd.Flags().StringVarP(&hardlinkPrim, "prim", "p", "", "主要文件路径")
	createHardlinkCmd.Flags().StringVarP(&hardlinkSeco, "seco", "s", "", "次要文件路径")
	createHardlinkCmd.Flags().BoolVar(&createForce, "force", false, "强制覆盖已存在的文件")
	createHardlinkCmd.Flags().StringVar(&createDevice, "device", "", "设备名称，用于后续设备过滤检查")
	createHardlinkCmd.MarkFlagRequired("prim")
	createHardlinkCmd.MarkFlagRequired("seco")
}

func runCreateSymlink(cmd *cobra.Command, args []string) error {
	// 获取有效工作目录（支持 -w/--work-dir 全局参数）
	workDir, err := GetEffectiveWorkDir()
	if err != nil {
		return err
	}

	// 创建 logger
	log, err := logger.NewLogger("", 0, logger.INFO)
	if err != nil {
		return fmt.Errorf("创建日志器失败：%w", err)
	}
	defer log.Close()

	// 规范化路径
	realPath, err := pathutil.NormalizePath(symlinkReal)
	if err != nil {
		return fmt.Errorf("规范化真实路径失败：%w", err)
	}
	fakePath, err := pathutil.NormalizePath(symlinkFake)
	if err != nil {
		return fmt.Errorf("规范化链接路径失败：%w", err)
	}

	// 转换为绝对路径
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return fmt.Errorf("获取真实路径绝对路径失败：%w", err)
	}
	fakePath, err = filepath.Abs(fakePath)
	if err != nil {
		return fmt.Errorf("获取链接路径绝对路径失败：%w", err)
	}

	// 如果目标已存在且启用了 force，则删除
	if linkop.PathExists(fakePath) {
		if createForce {
			if err := os.Remove(fakePath); err != nil {
				return fmt.Errorf("删除已存在的文件失败：%w", err)
			}
			interact.PrintInfo("已删除已存在的文件：“%s”", fakePath)
		} else {
			return fmt.Errorf("链接路径已存在：“%s”（使用“--force”强制覆盖）", fakePath)
		}
	}

	// 记录日志
	log.Info("创建符号链接：“%s”->“%s”", realPath, fakePath)

	// 确保链接目录存在
	if err := pathutil.EnsureDirExists(fakePath); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// 创建符号链接
	if err := linkop.CreateSymlink(realPath, fakePath); err != nil {
		log.Error("创建符号链接失败：%v", err)
		return err
	}

	interact.PrintSuccess("符号链接创建成功")
	interact.PrintInfo("真实路径：“%s”", realPath)
	interact.PrintInfo("链接路径：“%s”", fakePath)

	// 保存记录
	return saveSymlinkRecord(workDir, realPath, fakePath, log)
}

func runCreateHardlink(cmd *cobra.Command, args []string) error {
	// 获取有效工作目录（支持 -w/--work-dir 全局参数）
	workDir, err := GetEffectiveWorkDir()
	if err != nil {
		return err
	}

	// 创建 logger
	log, err := logger.NewLogger("", 0, logger.INFO)
	if err != nil {
		return fmt.Errorf("创建日志器失败：%w", err)
	}
	defer log.Close()

	// 规范化路径
	primPath, err := pathutil.NormalizePath(hardlinkPrim)
	if err != nil {
		return fmt.Errorf("规范化主要文件路径失败：%w", err)
	}
	secoPath, err := pathutil.NormalizePath(hardlinkSeco)
	if err != nil {
		return fmt.Errorf("规范化次要文件路径失败：%w", err)
	}

	// 转换为绝对路径
	primPath, err = filepath.Abs(primPath)
	if err != nil {
		return fmt.Errorf("获取主要文件绝对路径失败：%w", err)
	}
	secoPath, err = filepath.Abs(secoPath)
	if err != nil {
		return fmt.Errorf("获取次要文件绝对路径失败：%w", err)
	}

	// 如果目标已存在且启用了 force，则删除
	if linkop.PathExists(secoPath) {
		if createForce {
			if err := os.Remove(secoPath); err != nil {
				return fmt.Errorf("删除已存在的文件失败：%w", err)
			}
			interact.PrintInfo("已删除已存在的文件：“%s”", secoPath)
		} else {
			return fmt.Errorf("次要文件路径已存在：“%s”（使用“--force”强制覆盖）", secoPath)
		}
	}

	// 记录日志
	log.Info("创建硬链接：“%s”->“%s”", primPath, secoPath)

	// 确保目录存在
	if err := pathutil.EnsureDirExists(secoPath); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// 创建硬链接
	if err := linkop.CreateHardlink(primPath, secoPath); err != nil {
		log.Error("创建硬链接失败：%v", err)
		return err
	}

	interact.PrintSuccess("硬链接创建成功")
	interact.PrintInfo("主要文件：“%s”", primPath)
	interact.PrintInfo("次要文件：“%s”", secoPath)

	// 保存记录
	return saveHardlinkRecord(workDir, primPath, secoPath, log)
}

func saveSymlinkRecord(workDir, realPath, fakePath string, log *logger.Logger) error {
	// 创建 storage
	st := storage.NewStorage(workDir)

	// 加载 storage
	if err := st.Load(); err != nil {
		log.Warn("加载“file-link-manager-links.json”失败：%v", err)
		// 继续执行，不返回错误
	}

	// 计算相对路径
	relPath, err := pathutil.ToRelative(workDir, realPath)
	if err != nil {
		log.Warn("计算相对路径失败：%v", err)
		relPath = realPath // 使用绝对路径作为后备
	}

	// 添加记录
	osType := pathutil.GetCurrentOS()
	// 确定设备名称：空字符串表示通用文件
	deviceName := "common"
	if createDevice != "" {
		deviceName = createDevice
	}
	record := storage.SymlinkRecord{
		RealRelative: relPath,
		FakeAbsolute: fakePath,
		Device:       deviceName, // 添加设备信息
	}

	if err := st.AddSymlink(osType, record); err != nil {
		log.Error("添加记录失败：%v", err)
		return fmt.Errorf("添加记录失败：%w", err)
	}

	// 去重
	st.DeduplicateSymlinks(osType)

	// 保存
	if err := st.Save(); err != nil {
		log.Error("保存“file-link-manager-links.json”失败：%v", err)
		return fmt.Errorf("保存记录失败：%w", err)
	}

	interact.PrintSuccess("记录已保存到“%s”", st.GetFilePath())

	// 记录当前位置到全局位置追踪
	if err := recordCurrentLocation(workDir); err != nil {
		log.Warn("记录位置失败：%v", err)
		// 不返回错误，因为链接已经创建成功
	}

	return nil
}

func saveHardlinkRecord(workDir, primPath, secoPath string, log *logger.Logger) error {
	// 创建 storage
	st := storage.NewStorage(workDir)

	// 加载 storage
	if err := st.Load(); err != nil {
		log.Warn("加载“file-link-manager-links.json”失败：%v", err)
	}

	// 计算相对路径
	relPath, err := pathutil.ToRelative(workDir, primPath)
	if err != nil {
		log.Warn("计算相对路径失败：%v", err)
		relPath = primPath
	}

	// 添加记录
	osType := pathutil.GetCurrentOS()
	// 确定设备名称：空字符串表示通用文件
	deviceName := "common"
	if createDevice != "" {
		deviceName = createDevice
	}
	record := storage.HardlinkRecord{
		PrimaryRelative:   relPath,
		SecondaryAbsolute: secoPath,
		Device:            deviceName, // 添加设备信息
	}

	if err := st.AddHardlink(osType, record); err != nil {
		log.Error("添加记录失败：%v", err)
		return fmt.Errorf("添加记录失败：%w", err)
	}

	// 去重
	st.DeduplicateHardlinks(osType)

	// 保存
	if err := st.Save(); err != nil {
		log.Error("保存“file-link-manager-links.json”失败：%v", err)
		return fmt.Errorf("保存记录失败：%w", err)
	}

	interact.PrintSuccess("记录已保存到“%s”", st.GetFilePath())

	// 记录当前位置到全局位置追踪
	if err := recordCurrentLocation(workDir); err != nil {
		log.Warn("记录位置失败：%v", err)
		// 不返回错误，因为链接已经创建成功
	}

	return nil
}

// recordCurrentLocation 记录当前工作目录到全局位置追踪
func recordCurrentLocation(workDir string) error {
	// 创建位置管理器
	locMgr, err := location.NewManager()
	if err != nil {
		return err
	}

	// 加载数据
	if err := locMgr.Load(); err != nil {
		return err
	}

	// 添加当前位置
	osType := pathutil.GetCurrentOS()
	if err := locMgr.AddLocation(osType, workDir); err != nil {
		return err
	}

	// 保存
	return locMgr.Save()
}
