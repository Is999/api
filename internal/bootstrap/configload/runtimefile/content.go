package runtimefile

import (
	"strings"

	"api/internal/bootstrap/configload/validators"

	"github.com/Is999/go-utils/errors"
	yaml "go.yaml.in/yaml/v2"
)

// validatedContent 校验并返回当前版本声明的运行期配置块。
func validatedContent(path string) ([]byte, map[string]struct{}, string, error) {
	// 原始文件先做严格字段校验，避免宽松解码吞掉拼写错误。
	data, fingerprint, err := ReadSource(path)
	if err != nil {
		return nil, nil, "", errors.Wrapf(err, "读取运行期外部配置失败 file=%s", path)
	}
	keys := make(map[string]struct{})
	if len(strings.TrimSpace(string(data))) == 0 {
		return []byte("{}\n"), keys, fingerprint, nil
	}
	if err = validators.ValidateKnownYAMLFields(data, file{}); err != nil {
		return nil, nil, "", errors.Wrapf(err, "运行期外部配置字段校验失败 file=%s", path)
	}
	var root yaml.MapSlice
	if err = yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, "", errors.Wrapf(err, "解析运行期外部配置失败 file=%s", path)
	}
	// 顶层配置段必须使用唯一且已登记的规范名称。
	knownKeys := sectionKeys()
	filtered := yaml.MapSlice{}
	for _, item := range root {
		key, ok := item.Key.(string)
		if !ok {
			return nil, nil, "", errors.Errorf("运行期外部配置顶层键必须是字符串 file=%s", path)
		}
		if key != strings.TrimSpace(key) {
			return nil, nil, "", errors.Errorf("运行期外部配置顶层键不能包含首尾空白 key=%q file=%s", key, path)
		}
		if _, known := knownKeys[key]; !known {
			return nil, nil, "", errors.Errorf("运行期外部配置包含未知顶层段 key=%s file=%s", key, path)
		}
		if _, duplicate := keys[key]; duplicate {
			return nil, nil, "", errors.Errorf("运行期外部配置顶层段重复 key=%s file=%s", key, path)
		}
		keys[key] = struct{}{}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return []byte("{}\n"), keys, fingerprint, nil
	}
	// 此处只提取已校验章节；启动期字段的保留由后续热加载边界处理。
	content, err := yaml.Marshal(filtered)
	if err != nil {
		return nil, nil, "", errors.Wrapf(err, "提取运行期外部配置失败 file=%s", path)
	}
	return content, keys, fingerprint, nil
}
