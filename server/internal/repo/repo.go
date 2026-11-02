// Package repo 在测试中定位仓库根目录（protocol/ 夹具与机器件以仓库根相对
// 路径引用，spec §7 消费契约：路径稳定）。
package repo

import (
	"errors"
	"os"
	"path/filepath"
)

// Root 返回仓库根目录（包含 protocol/spec.md 与 server/go.mod 的目录）。
// 自当前工作目录逐级向上探测；未找到返回错误（调用方应 t.Fatalf 而非跳过，
// 夹具缺失属于车道级阻断）。
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "server", "go.mod")) && fileExists(filepath.Join(dir, "protocol", "spec.md")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repo: 未找到仓库根（缺少 server/go.mod 与 protocol/spec.md）")
		}
		dir = parent
	}
}

// ProtocolPath 返回 protocol/ 下相对路径 p 的绝对路径。
func ProtocolPath(p string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "protocol", filepath.FromSlash(p)), nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
