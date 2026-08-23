// Package secureid 统一生成服务运行期使用的密码学随机标识。
package secureid

import (
	"crypto/rand"
	"encoding/hex"
	"io"

	"github.com/Is999/go-utils/errors"
)

// NewHex 生成 byteCount 个随机字节对应的十六进制标识；系统随机源失败时返回错误，禁止降级为时间戳。
func NewHex(byteCount int) (string, error) {
	return newHex(rand.Reader, byteCount)
}

// newHex 接受可替换随机源，便于验证短读和熵源失败分支。
func newHex(reader io.Reader, byteCount int) (string, error) {
	if reader == nil || byteCount <= 0 {
		return "", errors.New("安全随机标识参数非法")
	}
	buf := make([]byte, byteCount)
	// 随机源短读不能降低标识熵，必须填满约定字节数才允许返回。
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", errors.Wrap(err, "读取系统安全随机源失败")
	}
	return hex.EncodeToString(buf), nil
}
