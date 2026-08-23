package runtimefile

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Is999/go-utils/errors"
)

// ReadSource 读取一份配置文件字节与对应指纹，主文件、外置文件和轮询共用同一来源规则。
func ReadSource(file string) ([]byte, string, error) {
	if file == "" || file != strings.TrimSpace(file) {
		return nil, "", errors.New("配置文件路径不能为空或包含首尾空白")
	}
	cleanFile := filepath.Clean(file)
	// 固定文件句柄后再读内容，原子替换路径不会把另一版本混入本轮字节。
	handle, err := os.Open(cleanFile)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	data, err := io.ReadAll(handle)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	// 原地改写不能形成可确认的快照；拒绝本轮并由下次重载重新读取。
	after, err := handle.Stat()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	if info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, "", errors.New("配置文件在读取期间发生变化")
	}
	// 指纹的内容摘要始终来自上述字节，路径和元信息只用于发现 ConfigMap 替换。
	realPath, err := filepath.EvalSymlinks(cleanFile)
	if err != nil {
		realPath = cleanFile
	}
	fingerprint := fmt.Sprintf("%s|%d|%d|%x", realPath, info.Size(), info.ModTime().UnixNano(), sha256.Sum256(data))
	return data, fingerprint, nil
}
