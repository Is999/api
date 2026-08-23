package runtimefile

import (
	"path/filepath"
	"strings"

	"api/internal/config"
)

// IncludePaths 返回主配置声明的外部配置文件解析结果。
func IncludePaths(mainFile string, files config.ConfigFilesConfig) []string {
	if files.Runtime == "" || files.Runtime != strings.TrimSpace(files.Runtime) {
		return nil
	}
	return []string{resolveIncludePath(mainFile, files.Runtime)}
}

// resolveIncludePath 相对主配置所在目录解析外置路径，不受进程工作目录影响。
func resolveIncludePath(mainFile string, include string) string {
	if include == "" || filepath.IsAbs(include) {
		return filepath.Clean(include)
	}
	baseDir := filepath.Dir(filepath.Clean(mainFile))
	return filepath.Clean(filepath.Join(baseDir, include))
}
