package updater

import (
	"testing"
)

// TestProxyList_Default 未设置环境变量时回退到默认代理
func TestProxyList_Default(t *testing.T) {
	t.Setenv("FLK_GH_PROXY", "")
	list := proxyList()
	if len(list) != 1 || list[0] != "https://gh-proxy.org/" {
		t.Fatalf("默认代理解析不符: %v", list)
	}
}

// TestProxyList_Single 单个代理应被原样使用并补齐尾斜杠
func TestProxyList_Single(t *testing.T) {
	t.Setenv("FLK_GH_PROXY", "https://my-proxy.example.com")
	list := proxyList()
	if len(list) != 1 || list[0] != "https://my-proxy.example.com/" {
		t.Fatalf("单代理解析不符: %v", list)
	}
}

// TestProxyList_Multi 逗号分隔多个代理，按顺序作为故障转移优先级
func TestProxyList_Multi(t *testing.T) {
	t.Setenv("FLK_GH_PROXY", "https://a.example.com/, https://b.example.com")
	list := proxyList()
	if len(list) != 2 {
		t.Fatalf("多代理数量不符: %v", list)
	}
	if list[0] != "https://a.example.com/" || list[1] != "https://b.example.com/" {
		t.Fatalf("多代理顺序或尾斜杠不符: %v", list)
	}
}

// TestProxyList_EmptyEntries 空段与空白应被忽略
func TestProxyList_EmptyEntries(t *testing.T) {
	t.Setenv("FLK_GH_PROXY", ",, https://x.example.com ,, ")
	list := proxyList()
	if len(list) != 1 || list[0] != "https://x.example.com/" {
		t.Fatalf("空段过滤不符: %v", list)
	}
}
