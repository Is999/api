package keys

import (
	"fmt"
	"strings"
)

// OpsReplayNonceRedisKey 返回内网运维请求 nonce 的完整缓存键。
func OpsReplayNonceRedisKey(nonce string) string {
	if nonce == "" || nonce != strings.TrimSpace(nonce) {
		return ""
	}
	return WithPrefix(fmt.Sprintf(OpsReplayNonce, nonce))
}
