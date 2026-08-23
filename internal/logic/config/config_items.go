package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	codes "api/common/codes"
	i18n "api/common/i18n"
	appconfig "api/internal/config"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
	yaml "go.yaml.in/yaml/v2"
)

const (
	configItemDefaultPage     = 1        // 配置项查询默认页码
	configItemMaskPlaceholder = "****"   // 敏感配置值统一脱敏占位符
	configItemValueBool       = "bool"   // 配置项布尔值类型
	configItemValueList       = "list"   // 配置项列表值类型
	configItemValueNull       = "null"   // 配置项空值类型
	configItemValueNumber     = "number" // 配置项数字值类型
	configItemValueObject     = "object" // 配置项对象值类型
	configItemValueString     = "string" // 配置项字符串值类型
)

// maskedConfigView 聚合扁平配置项、分组统计和脱敏 YAML 快照。
type maskedConfigView struct {
	items          []types.ConfigItem        // 扁平化后的配置项列表
	sections       []types.ConfigSectionStat // 按一级配置段聚合的统计
	sensitiveTotal int                       // 命中敏感规则的配置项数量
	snapshotYAML   string                    // 脱敏后的完整运行配置快照
}

var (
	configItemIPv4Pattern = regexp.MustCompile(`(?:^|[^\d])(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)(?:$|[^\d])`)       // 识别配置值中的 IPv4 地址片段
	configItemHostPattern = regexp.MustCompile(`(?i)\b(?:localhost|(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{1,62}):\d{2,5}\b`) // 识别配置值中的主机端口片段
)

// ConfigReloadItems 查询当前 API 进程内已经生效的运行态配置快照。
func (l *SystemLogic) ConfigReloadItems(req *types.ConfigItemQueryReq) *types.BizResult {
	// 查询参数先完成边界校验，避免无效分页触发快照构建。
	if l.Svc == nil {
		return types.NewBizResult(codes.InternalError).
			SetI18nMessage(i18n.MsgKeyInternalError).
			WithError(errors.New("ServiceContext 未初始化"))
	}
	if req == nil {
		req = &types.ConfigItemQueryReq{}
	}
	if err := req.Validate(); err != nil {
		return types.ParamErrorResult(err)
	}

	// 完整配置只在内存中构建脱敏视图和运行期 YAML，不回读配置文件。
	currentConfig := l.Svc.CurrentConfig()
	view, err := buildMaskedConfigView(currentConfig)
	if err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "SystemLogic.ConfigReloadItems").ToBizResult()
	}
	runtimeYAML, err := buildMaskedRuntimeYAML(currentConfig)
	if err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "SystemLogic.ConfigReloadItems.runtime").ToBizResult()
	}
	// 过滤后再分页，返回总数与当前页始终基于同一快照。
	filtered := filterConfigItems(view.items, req.Keyword, req.SensitiveOnly)
	pageItems := paginateConfigItems(filtered, req.Page, req.PageSize)
	status := l.Svc.CurrentHotReloadStatus()

	// 来源元数据取最近热加载状态；待重启配置不会替换上方进程内的实际生效值。
	return types.NewBizResult(codes.FetchSuccess).
		SetI18nMessage(i18n.MsgKeyFetchSuccess).
		WithData(&types.ConfigItemQueryResp{
			Keyword:        req.Keyword,
			SensitiveOnly:  req.SensitiveOnly,
			Page:           req.Page,
			PageSize:       req.PageSize,
			Total:          int64(len(filtered)),
			TotalItems:     len(view.items),
			SensitiveTotal: view.sensitiveTotal,
			Sections:       view.sections,
			Source: types.ConfigSourceMeta{
				Source:            "runtime_snapshot",
				ConfigFile:        status.ConfigFile,
				RuntimeFile:       configRuntimeFilePath(status.ConfigFile, currentConfig.ConfigFiles.Runtime),
				ConfigVersion:     status.ConfigVersion,
				LastStatus:        status.LastStatus,
				LastTriggerSource: status.LastTriggerSource,
				LastReloadAt:      formatConfigTime(status.LastReloadAt),
				LastSuccessAt:     formatConfigTime(status.LastSuccessAt),
				RestartRequired:   status.RestartRequired,
				RestartReason:     status.RestartReason,
			},
			SnapshotYAML: view.snapshotYAML,
			RuntimeYAML:  runtimeYAML,
			Items:        pageItems,
		})
}

// buildMaskedConfigView 将配置结构按 json tag 转为稳定的扁平路径和脱敏 YAML 快照。
func buildMaskedConfigView(cfg appconfig.Config) (*maskedConfigView, error) {
	// 与局部配置视图共用 JSON tag 和数字精度规则，不另外维护一套转换。
	root, err := decodeConfigJSONValue(cfg)
	if err != nil {
		return nil, errors.Tag(err)
	}

	items := make([]types.ConfigItem, 0, 128)
	snapshot := appendConfigItems(&items, "", root, false)
	// 按路径固定顺序，避免 map 遍历顺序改变分页内容。
	slices.SortFunc(items, func(left, right types.ConfigItem) int {
		return strings.Compare(left.Path, right.Path)
	})
	snapshotYAML, err := marshalConfigSnapshotYAML(snapshot)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return &maskedConfigView{
		items:          items,
		sections:       buildConfigSectionStats(items),
		sensitiveTotal: countSensitiveConfigItems(items),
		snapshotYAML:   snapshotYAML,
	}, nil
}

// buildMaskedRuntimeYAML 按 API 支持外置的运行期配置段展示已并入运行态的配置。
func buildMaskedRuntimeYAML(cfg appconfig.Config) (string, error) {
	// 未配置的整段不展示；已展示段内的 0 和 false 保留，不能误读为缺省启用。
	view := make(map[string]any, 5)
	if !reflect.DeepEqual(cfg.Auth, appconfig.AuthConfig{}) {
		view["auth"] = cfg.Auth
	}
	if !reflect.DeepEqual(cfg.HotReload, appconfig.HotReloadConfig{}) {
		view["hot_reload"] = cfg.HotReload
	}
	if !reflect.DeepEqual(cfg.Security, appconfig.SecurityConfig{}) {
		view["security"] = cfg.Security
	}
	if !reflect.DeepEqual(cfg.Collector, appconfig.CollectorConfig{}) {
		view["collector"] = cfg.Collector
	}
	if !reflect.DeepEqual(cfg.Ops, appconfig.OpsConfig{}) {
		view["ops"] = cfg.Ops
	}
	return buildMaskedConfigYAML(view)
}

// buildMaskedConfigYAML 将指定配置块转换为脱敏后的 YAML 文本。
func buildMaskedConfigYAML(value any) (string, error) {
	root, err := decodeConfigJSONValue(value)
	if err != nil {
		return "", errors.Tag(err)
	}
	items := make([]types.ConfigItem, 0, 32)
	snapshot := appendConfigItems(&items, "", root, false)
	return marshalConfigSnapshotYAML(snapshot)
}

// decodeConfigJSONValue 通过 JSON 中间形态统一结构体 tag 和数字类型。
func decodeConfigJSONValue(value any) (any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Wrap(err, "序列化配置块失败")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	// 保留 JSON 数字文本，避免大整数经 float64 中转后丢失精度。
	decoder.UseNumber()

	var root any
	if err = decoder.Decode(&root); err != nil {
		return nil, errors.Wrap(err, "解析配置块快照失败")
	}
	return root, nil
}

// appendConfigItems 展开配置树并传播父级敏感标记；地址映射的动态 key 也需脱敏。
func appendConfigItems(items *[]types.ConfigItem, path string, value any, inheritedSensitive bool) any {
	currentSensitive := inheritedSensitive || isSensitiveConfigPath(path) || isAddressConfigPath(path)
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			appendConfigLeaf(items, path, configItemValueObject, "{}", currentSensitive)
			return map[string]any{}
		}
		// 原始键排序后再分配脱敏序号，同一地址在重复查询中保持同一展示路径。
		keys := slices.Sorted(maps.Keys(typed))
		maskedMap := make(map[string]any, len(typed))
		for index, key := range keys {
			displayKey := key
			// addr_map 等动态映射把地址放在 YAML key 中；序号同时避免不同地址脱敏后覆盖同一个展示键。
			if isAddressConfigPath(path) && isConfigAddressLike(key) {
				displayKey = fmt.Sprintf("%s#%d", maskConfigString(key), index+1)
			}
			maskedMap[displayKey] = appendConfigItems(items, joinConfigPath(path, displayKey), typed[key], currentSensitive)
		}
		return maskedMap
	case []any:
		if len(typed) == 0 {
			appendConfigLeaf(items, path, configItemValueList, "[]", currentSensitive)
			return []any{}
		}
		maskedList := make([]any, 0, len(typed))
		for index, item := range typed {
			maskedList = append(maskedList, appendConfigItems(items, fmt.Sprintf("%s[%d]", path, index), item, currentSensitive))
		}
		return maskedList
	default:
		item := buildConfigLeaf(path, configValueType(value), formatConfigValue(value), currentSensitive)
		appendConfigLeafItem(items, item)
		return configYAMLLeafValue(value, item)
	}
}

// appendConfigLeaf 写入最终叶子节点，敏感值只保留首尾少量字符。
func appendConfigLeaf(items *[]types.ConfigItem, path string, valueType string, rawValue string, inheritedSensitive bool) {
	appendConfigLeafItem(items, buildConfigLeaf(path, valueType, rawValue, inheritedSensitive))
}

// buildConfigLeaf 构造单个配置叶子节点，并在必要时脱敏展示值。
func buildConfigLeaf(path string, valueType string, rawValue string, inheritedSensitive bool) types.ConfigItem {
	if strings.TrimSpace(path) == "" {
		return types.ConfigItem{}
	}
	sensitive := inheritedSensitive || shouldMaskConfigValue(path, rawValue)
	displayValue := rawValue
	if sensitive && isMaskableConfigValue(rawValue) {
		displayValue = maskConfigString(rawValue)
	}
	return types.ConfigItem{
		Path:      path,
		Value:     displayValue,
		ValueType: valueType,
		Sensitive: sensitive,
	}
}

// appendConfigLeafItem 追加有效叶子节点，根节点空路径不参与展示。
func appendConfigLeafItem(items *[]types.ConfigItem, item types.ConfigItem) {
	if strings.TrimSpace(item.Path) == "" {
		return
	}
	*items = append(*items, item)
}

// filterConfigItems 只使用已脱敏展示值参与匹配，避免通过关键字搜索探测原始敏感值。
func filterConfigItems(items []types.ConfigItem, keyword string, sensitiveOnly bool) []types.ConfigItem {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	filtered := make([]types.ConfigItem, 0, len(items))
	for _, item := range items {
		if sensitiveOnly && !item.Sensitive {
			continue
		}
		if keyword != "" && !configItemMatches(item, keyword) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// configItemMatches 使用已脱敏字段做关键字匹配，避免命中原始敏感值。
func configItemMatches(item types.ConfigItem, keyword string) bool {
	return strings.Contains(strings.ToLower(item.Path), keyword) ||
		strings.Contains(strings.ToLower(item.ValueType), keyword) ||
		strings.Contains(strings.ToLower(item.Value), keyword)
}

// paginateConfigItems 对内存中的已脱敏配置项做安全分页。
func paginateConfigItems(items []types.ConfigItem, page int, pageSize int) []types.ConfigItem {
	if page <= 0 {
		page = configItemDefaultPage
	}
	if pageSize <= 0 {
		pageSize = len(items)
	}
	pageOffset := page - 1
	// 空结果的缺省页大小仍为零；先判空，再用除法阻止页码乘法溢出。
	if len(items) == 0 || pageOffset > len(items)/pageSize {
		return []types.ConfigItem{}
	}
	start := pageOffset * pageSize
	if start >= len(items) {
		return []types.ConfigItem{}
	}
	end := start + min(pageSize, len(items)-start)
	return items[start:end]
}

// marshalConfigSnapshotYAML 压缩空容器和空文本，保留数字零值与关闭状态。
func marshalConfigSnapshotYAML(snapshot any) (string, error) {
	compactSnapshot, ok := compactConfigYAMLValue(snapshot)
	if !ok {
		return "", nil
	}
	payload, err := yaml.Marshal(compactSnapshot)
	if err != nil {
		return "", errors.Wrap(err, "序列化配置 YAML 失败")
	}
	text := strings.TrimRight(string(payload), "\n")
	if text == "" {
		return "", nil
	}
	return text + "\n", nil
}

// compactConfigYAMLValue 递归移除空容器和空文本，不改变叶子值的类型或零值。
func compactConfigYAMLValue(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		// 空子树不进入展示 YAML；此视图仅供查看，不能直接作为启动配置回写。
		result := make(map[string]any, len(typed))
		for _, key := range slices.Sorted(maps.Keys(typed)) {
			child, ok := compactConfigYAMLValue(typed[key])
			if ok {
				result[key] = child
			}
		}
		return result, len(result) > 0
	case []any:
		// 列表只移除空元素，保留剩余元素的原始相对顺序。
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			child, ok := compactConfigYAMLValue(item)
			if ok {
				result = append(result, child)
			}
		}
		return result, len(result) > 0
	case string:
		return typed, strings.TrimSpace(typed) != ""
	case nil:
		return nil, false
	default:
		return typed, true
	}
}

// buildConfigSectionStats 汇总顶层配置块数量和敏感项数量。
func buildConfigSectionStats(items []types.ConfigItem) []types.ConfigSectionStat {
	statsByName := make(map[string]*types.ConfigSectionStat)
	for _, item := range items {
		name := configRootSection(item.Path)
		stat := statsByName[name]
		if stat == nil {
			stat = &types.ConfigSectionStat{Name: name}
			statsByName[name] = stat
		}
		stat.Total++
		if item.Sensitive {
			stat.SensitiveTotal++
		}
	}
	sections := make([]types.ConfigSectionStat, 0, len(statsByName))
	for _, stat := range statsByName {
		sections = append(sections, *stat)
	}
	// 聚合映射无序，输出前固定分组顺序，避免运维页面每次查询跳动。
	slices.SortFunc(sections, func(left, right types.ConfigSectionStat) int {
		return strings.Compare(left.Name, right.Name)
	})
	return sections
}

// countSensitiveConfigItems 统计后端判定需要脱敏的叶子配置项数量。
func countSensitiveConfigItems(items []types.ConfigItem) int {
	total := 0
	for _, item := range items {
		if item.Sensitive {
			total++
		}
	}
	return total
}

// configRootSection 提取配置路径的顶层块名称。
func configRootSection(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "root"
	}
	index := strings.IndexAny(path, ".[")
	if index < 0 {
		return path
	}
	return path[:index]
}

// configRuntimeFilePath 按主配置文件位置解析运行期配置文件路径。
func configRuntimeFilePath(configFile string, runtimeFile string) string {
	runtimeFile = strings.TrimSpace(runtimeFile)
	if runtimeFile == "" || filepath.IsAbs(runtimeFile) {
		return runtimeFile
	}
	configFile = strings.TrimSpace(configFile)
	if configFile == "" {
		return runtimeFile
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filepath.Clean(configFile)), runtimeFile))
}

// joinConfigPath 拼接对象路径或数组下标路径。
func joinConfigPath(parent string, child string) string {
	if parent == "" {
		return child
	}
	if strings.HasPrefix(child, "[") {
		return parent + child
	}
	return parent + "." + child
}

// shouldMaskConfigValue 判断单个配置值是否需要脱敏展示。
func shouldMaskConfigValue(path string, value string) bool {
	return isSensitiveConfigPath(path) ||
		isAddressConfigPath(path) ||
		isConfigAddressLike(value)
}

// isSensitiveConfigPath 判断路径叶子名称是否属于密钥、密码或 Token 类字段。
func isSensitiveConfigPath(path string) bool {
	leaf := configPathLeaf(path)
	if leaf == "" {
		return false
	}
	switch leaf {
	case "access_key", "aes_key", "aes_key_ref", "app_key",
		"jwt_secret", "password", "passwd", "private_key", "public_key", "pwd",
		"secret", "secret_key", "secret_ref", "token", "webhook_url", "webhook_url_ref":
		return true
	case "key_version", "stable_version", "gray_version", "version":
		return false
	}
	for _, token := range []string{"password", "passwd", "private_key", "public_key", "secret", "token"} {
		if strings.Contains(leaf, token) {
			return true
		}
	}
	return strings.Contains(leaf, "salt")
}

// isAddressConfigPath 判断路径叶子名称是否属于地址、连接串或 URL 类字段。
func isAddressConfigPath(path string) bool {
	leaf := configPathLeaf(path)
	if leaf == "" {
		return false
	}
	switch leaf {
	case "addr", "addr_map", "addrs", "address", "addresses", "broker", "brokers",
		"config_reload_allowed_ips", "data_source", "datasource", "domain", "dsn", "endpoint", "endpoints",
		"host", "hosts", "read_data_sources", "uri", "url", "webhook_url",
		"trusted_proxies", "write_data_source":
		return true
	}
	for _, token := range []string{"_addr", "_address", "_broker", "_data_source", "_datasource", "_domain", "_dsn", "_endpoint", "_host", "_uri", "_url"} {
		if strings.Contains(leaf, token) {
			return true
		}
	}
	return false
}

// configPathLeaf 归一化配置路径叶子，便于敏感规则匹配。
func configPathLeaf(path string) string {
	leaf := strings.TrimSpace(path)
	for strings.HasSuffix(leaf, "]") {
		// 数组下标不属于字段名，brokers[0] 仍沿用 brokers 的地址脱敏规则。
		index := strings.LastIndex(leaf, "[")
		if index < 0 {
			break
		}
		leaf = leaf[:index]
	}
	if dot := strings.LastIndex(leaf, "."); dot >= 0 {
		leaf = leaf[dot+1:]
	}
	leaf = strings.ToLower(strings.TrimSpace(leaf))
	leaf = strings.ReplaceAll(leaf, "-", "_")
	return leaf
}

// isConfigAddressLike 识别常见 URL、host:port、IP 和数据库 DSN。
func isConfigAddressLike(value string) bool {
	text := strings.TrimSpace(value)
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "@tcp(") || strings.Contains(lower, "@udp(") {
		return true
	}
	if parsed, err := url.Parse(text); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return true
	}
	if host, _, err := net.SplitHostPort(text); err == nil && strings.TrimSpace(host) != "" {
		return true
	}
	// ParseIP/ParseCIDR 补齐正则无法可靠识别的纯 IPv6 与 IPv6 网段，避免内网拓扑进入公开配置快照。
	if net.ParseIP(strings.Trim(text, "[]")) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(text); err == nil {
		return true
	}
	return configItemIPv4Pattern.MatchString(text) ||
		configItemHostPattern.MatchString(text)
}

// isMaskableConfigValue 判断当前展示值是否适合做首尾保留脱敏。
func isMaskableConfigValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	switch trimmed {
	case "", "[]", "{}", "null":
		return false
	default:
		return true
	}
}

// maskConfigString 对敏感字符串做首尾保留脱敏，避免泄露地址和密钥原文。
func maskConfigString(value string) string {
	runes := []rune(strings.TrimSpace(value))
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) <= 4:
		return configItemMaskPlaceholder
	case len(runes) <= 8:
		return string(runes[:1]) + configItemMaskPlaceholder + string(runes[len(runes)-1:])
	case len(runes) <= 16:
		return string(runes[:2]) + configItemMaskPlaceholder + string(runes[len(runes)-2:])
	default:
		return string(runes[:4]) + configItemMaskPlaceholder + string(runes[len(runes)-4:])
	}
}

// configYAMLLeafValue 返回 YAML 快照中的叶子值，敏感项使用脱敏展示值。
func configYAMLLeafValue(value any, item types.ConfigItem) any {
	if item.Sensitive {
		return item.Value
	}
	switch typed := value.(type) {
	case json.Number:
		return configNumberYAMLValue(typed)
	default:
		return value
	}
}

// configNumberYAMLValue 将 JSON 数字还原为 YAML 友好的整数或浮点数。
func configNumberYAMLValue(value json.Number) any {
	if numberValue, err := value.Int64(); err == nil {
		return numberValue
	}
	if numberValue, err := value.Float64(); err == nil {
		return numberValue
	}
	return value.String()
}

// configValueType 返回前端用于筛选和展示的稳定值类型。
func configValueType(value any) string {
	switch value.(type) {
	case nil:
		return configItemValueNull
	case bool:
		return configItemValueBool
	case json.Number, float32, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return configItemValueNumber
	case []any:
		return configItemValueList
	case map[string]any:
		return configItemValueObject
	default:
		return configItemValueString
	}
}

// formatConfigValue 将叶子配置值转成表格展示字符串。
func formatConfigValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}

// formatConfigTime 统一输出热加载时间；零值返回空字符串。
func formatConfigTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
