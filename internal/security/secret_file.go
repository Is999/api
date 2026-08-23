package security

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// maxSecretFileBytes 限制单份密码学材料为 64 KiB，避免错误引用消耗无界内存。
const maxSecretFileBytes = 64 << 10

// ReadSecretFile 仅读取普通文件；允许符号链接，路径与空内容规则仍由调用方校验。
func ReadSecretFile(path string) ([]byte, error) {
	// 先非阻塞打开再检查同一描述符，避免检查与打开之间被替换成 FIFO 后永久等待。
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("secret file must be a regular file: %s", path)
	}
	if info.Size() > maxSecretFileBytes {
		return nil, fmt.Errorf("secret file exceeds %d bytes: %s", maxSecretFileBytes, path)
	}
	// 读取期间文件仍可能增长，多读一字节用于拒绝超限而不是静默截断材料。
	body, err := io.ReadAll(io.LimitReader(file, maxSecretFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSecretFileBytes {
		return nil, fmt.Errorf("secret file exceeds %d bytes: %s", maxSecretFileBytes, path)
	}
	return body, nil
}
