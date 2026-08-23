package embedasset

import "strings"

// 文本资产剥离文件头注释时使用的特殊字符常量。
const (
	// unicodeBOM 表示 UTF-8 BOM，部分编辑器保存模板文件时可能写入，需要在剥离文件头前统一去掉。
	unicodeBOM = "\ufeff"
)

// StripLeadingLineComments 删除 go:embed 文本资产开头的连续行注释。
func StripLeadingLineComments(source string, prefixes ...string) string {
	normalizedSource := strings.TrimPrefix(source, unicodeBOM)
	commentPrefixes := normalizeCommentPrefixes(prefixes)
	if len(commentPrefixes) == 0 {
		return normalizedSource
	}
	lines := strings.SplitAfter(normalizedSource, "\n")
	bodyIndex := 0
	// 只有文件头实际出现注释才剥离空行，普通资产保留原始排版。
	strippedComment := false
	for bodyIndex < len(lines) {
		currentLine := strings.TrimSpace(lines[bodyIndex])
		if currentLine == "" {
			bodyIndex++
			continue
		}
		if hasLineCommentPrefix(currentLine, commentPrefixes) {
			strippedComment = true
			bodyIndex++
			continue
		}
		// 遇到第一行正文即结束，SQL/Lua 正文中的注释不属于维护头。
		break
	}
	if !strippedComment {
		return normalizedSource
	}
	executableText := strings.Join(lines[bodyIndex:], "")
	return strings.TrimLeft(executableText, "\r\n")
}

// normalizeCommentPrefixes 清洗调用方声明的行注释前缀。
func normalizeCommentPrefixes(prefixes []string) []string {
	normalized := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		item := strings.TrimSpace(prefix)
		if item == "" {
			continue
		}
		normalized = append(normalized, item)
	}
	return normalized
}

// hasLineCommentPrefix 判断当前行是否命中允许剥离的文件头行注释前缀。
func hasLineCommentPrefix(line string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
