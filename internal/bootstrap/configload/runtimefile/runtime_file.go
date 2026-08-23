package runtimefile

import (
	"strings"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/conf"
)

// Apply 合并主配置声明的外置运行期文件，并返回该轮实际读取的指纹。
func Apply(mainFile string, cfg *config.Config) (string, error) {
	if cfg == nil || cfg.ConfigFiles.Runtime == "" {
		return "", nil
	}
	if cfg.ConfigFiles.Runtime != strings.TrimSpace(cfg.ConfigFiles.Runtime) {
		return "", errors.Errorf("config_files.runtime 不能包含首尾空白")
	}
	return apply(resolveIncludePath(mainFile, cfg.ConfigFiles.Runtime), cfg)
}

// apply 只发布成功解码的外置章节，返回的指纹来自同一份已校验字节。
func apply(path string, cfg *config.Config) (string, error) {
	content, keys, fingerprint, err := validatedContent(path)
	if err != nil {
		return "", errors.Tag(err)
	}
	var ext file
	if err = conf.LoadFromYamlBytes(content, &ext); err != nil {
		return "", errors.Wrapf(err, "加载运行期外部配置失败 file=%s", path)
	}
	// 仅显式声明的章节整体覆盖；未声明章节保留主文件，不做字段级合并。
	for _, spec := range sectionSpecs() {
		if _, ok := keys[spec.Key]; ok {
			spec.apply(cfg, ext)
		}
	}
	return fingerprint, nil
}
