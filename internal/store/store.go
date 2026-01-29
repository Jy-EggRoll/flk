package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type SymEntry struct {
	Real string `json:"real"`
	Fake string `json:"fake"`
}

type HardEntry struct {
	Prim string `json:"prim"`
	Seco string `json:"seco"`
}

func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "flk", "flk-store.json"), nil
}

func LinkToFile(platform, device, ltype, REALorPRIM, FAKEorSECO string) error {
	// 默认值
	if platform == "" {
		platform = runtime.GOOS
	}
	if device == "" {
		device = "all"
	}
	if ltype == "" {
		return fmt.Errorf("必须指定链接类型")
	}

	// 先把路径缩写 home 为 ~，以满足你的存储约定
	REALorPRIM = shrinkHome(REALorPRIM)
	FAKEorSECO = shrinkHome(FAKEorSECO)

	path, err := DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("获取默认路径失败：%w", err)
	}


var data 

    if ltype == "symlink" {
        // 读取现有文件（若不存在则从空结构开始）
        data = make(map[string]map[string]map[string][]SymEntry)
        raw, err := os.ReadFile(path)
        if err != nil {
            if !os.IsNotExist(err) {
                return fmt.Errorf("读取文件失败：%w", err)
            }
            // 文件不存在：继续用空 data
        } else if len(raw) > 0 {
            if err := json.Unmarshal(raw, &data); err != nil {
                return fmt.Errorf("解析 JSON 失败：%w", err)
            }
        }
        if data[platform] == nil {
            data[platform] = make(map[string]map[string][]SymEntry)
        }
        if data[platform][device] == nil {
            data[platform][device] = make(map[string][]SymEntry)
        }
        if data[platform][device][ltype] == nil {
            data[platform][device][ltype] = []SymEntry{}
        }
    } else {
        // 读取现有文件（若不存在则从空结构开始）
        data = make(map[string]map[string]map[string][]HardEntry)
        raw, err := os.ReadFile(path)
        if err != nil {
            if !os.IsNotExist(err) {
                return fmt.Errorf("读取文件失败：%w", err)
            }
            // 文件不存在：继续用空 data
        } else if len(raw) > 0 {
            if err := json.Unmarshal(raw, &data); err != nil {
                return fmt.Errorf("解析 JSON 失败：%w", err)
            }
        }
        if data[platform] == nil {
            data[platform] = make(map[string]map[string][]HardEntry)
        }
        if data[platform][device] == nil {
            data[platform][device] = make(map[string][]HardEntry)
        }
        if data[platform][device][ltype] == nil {
            data[platform][device][ltype] = []HardEntry{}
        }
    }


	// 合并：保证 real 唯一，存在则更新 fake，否则 append
	entries := data[platform][device][ltype]
	updated := false

	if ltype == "symlink" {
		for i := range entries {
			if entries[i].Real == REALorPRIM {
				entries[i].Fake = FAKEorSECO
				updated = true
				break
			}
		}
		if !updated {
			entries = append(entries, SymEntry{Real: REALorPRIM, Fake: FAKEorSECO})
		}

		// 去重（以防已有重复），并按 Real 排序，保持稳定输出
		m := make(map[string]SymEntry, len(entries))
		for _, e := range entries {
			m[e.Real] = e // 保证以最后一次为准
		}
		uniq := make([]SymEntry, 0, len(m))
		for _, e := range m {
			uniq = append(uniq, e)
		}
		sort.Slice(uniq, func(i, j int) bool { return uniq[i].Real < uniq[j].Real })

		data[platform][device][ltype] = uniq
	} else {
        for i := range entries {
			if entries[i].Prim == REALorPRIM {
				entries[i].Seco = FAKEorSECO
				updated = true
				break
			}
		}
		if !updated {
			entries = append(entries, SymEntry{Real: REALorPRIM, Fake: FAKEorSECO})
		}

		// 去重（以防已有重复），并按 Real 排序，保持稳定输出
		m := make(map[string]SymEntry, len(entries))
		for _, e := range entries {
			m[e.Real] = e // 保证以最后一次为准
		}
		uniq := make([]SymEntry, 0, len(m))
		for _, e := range m {
			uniq = append(uniq, e)
		}
		sort.Slice(uniq, func(i, j int) bool { return uniq[i].Real < uniq[j].Real })

		data[platform][device][ltype] = uniq
    }

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建目录失败：%w", err)
	}

	// MarshalIndent 保持文件可读，写临时文件然后重命名
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON 序列化失败：%w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("写入临时文件失败：%w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("重命名临时文件失败：%w", err)
	}
	return nil
}

// 如果路径以用户主目录为前缀，则用 ~ 替换（只在前缀匹配时替换）
func shrinkHome(p string) string {
	if p == "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	home = filepath.Clean(home)
	cp := filepath.Clean(p)

	// Windows: 比较不区分大小写
	if runtime.GOOS == "windows" {
		home = strings.ToLower(home)
		cp = strings.ToLower(cp)
	}

	if cp == home {
		return "~"
	}
	sep := string(os.PathSeparator)
	if strings.HasPrefix(cp, home+sep) {
		// 恢复原始路径切片以保持分隔符风格
		orig := filepath.Clean(p)
		return "~" + orig[len(home):]
	}
	return p
}
