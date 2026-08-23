package config

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	keys "api/common/rediskeys"
	appconfig "api/internal/config"
	"api/internal/model"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestSysConfigKeyRegistryLookupAndCopy 确保 key 注册表可查找、去重且返回快照。
func TestSysConfigKeyRegistryLookupAndCopy(t *testing.T) {
	flagKey := OptionalSysConfigKey("featureFlag", model.SysConfigTypeBoolean, false, "功能开关")
	limitKey := RequiredSysConfigKey("maxLimit", model.SysConfigTypeInteger, "最大限制")
	registry, err := NewSysConfigKeyRegistry(flagKey, limitKey)
	if err != nil {
		t.Fatalf("NewSysConfigKeyRegistry() error = %v", err)
	}
	if _, ok := registry.Lookup("featureFlag"); !ok {
		t.Fatal("Lookup(featureFlag) should find key")
	}
	if _, ok := registry.Lookup(" featureFlag "); ok {
		t.Fatal("Lookup() must reject a non-canonical uuid")
	}
	items := registry.Items()
	items[0].UUID = "changed"
	if got := registry.Items()[0].UUID; got != "featureFlag" {
		t.Fatalf("registry first uuid = %s, want featureFlag", got)
	}
	if _, err = NewSysConfigKeyRegistry(flagKey, flagKey); err == nil {
		t.Fatal("expected duplicate sys_config key error")
	}
}

// TestSysConfigKeyRegistryRejectsInvalidDefault 确保默认值类型错误会被拦截。
func TestSysConfigKeyRegistryRejectsInvalidDefault(t *testing.T) {
	key := OptionalSysConfigKey("featureFlag", model.SysConfigTypeBoolean, "bad", "功能开关")
	if _, err := NewSysConfigKeyRegistry(key); err == nil {
		t.Fatal("expected invalid default value error")
	}
}

// TestSysConfigKeyRegistryRejectsNonCanonicalUUID 确保声明端不会把空白变体注册成同一个缓存键。
func TestSysConfigKeyRegistryRejectsNonCanonicalUUID(t *testing.T) {
	if _, err := NewSysConfigKeyRegistry(RequiredSysConfigKey(" featureFlag ", model.SysConfigTypeBoolean, "功能开关")); err == nil {
		t.Fatal("expected non-canonical sys_config uuid error")
	}
}

// TestTypedSysConfigGettersReadRedis 确保类型化读取优先使用 Redis 缓存。
func TestTypedSysConfigGettersReadRedis(t *testing.T) {
	// 按缓存存储格式预置全部支持的标量和容器类型。
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := newSysConfigLogicForKeyTest(client)
	seedTypedSysConfigCache(t, client, logicObj, "featureFlag", model.SysConfigTypeBoolean, "1")
	seedTypedSysConfigCache(t, client, logicObj, "maxLimit", model.SysConfigTypeInteger, "42")
	seedTypedSysConfigCache(t, client, logicObj, "welcomeText", model.SysConfigTypeString, `"hello"`)
	seedTypedSysConfigCache(t, client, logicObj, "ratio", model.SysConfigTypeFloat, "3.14")
	seedTypedSysConfigCache(t, client, logicObj, "objectValue", model.SysConfigTypeObject, `{"a":1}`)
	seedTypedSysConfigCache(t, client, logicObj, "arrayValue", model.SysConfigTypeArray, `[1,"b"]`)

	// 类型化 Getter 必须逐项还原为稳定的 Go 类型和值。
	flag, err := logicObj.GetBool(RequiredSysConfigKey("featureFlag", model.SysConfigTypeBoolean, "功能开关"))
	if err != nil || !flag {
		t.Fatalf("GetBool() = %v, %v; want true, nil", flag, err)
	}
	limit, err := logicObj.GetInt(RequiredSysConfigKey("maxLimit", model.SysConfigTypeInteger, "最大限制"))
	if err != nil || limit != 42 {
		t.Fatalf("GetInt() = %v, %v; want 42, nil", limit, err)
	}
	text, err := logicObj.GetString(RequiredSysConfigKey("welcomeText", model.SysConfigTypeString, "欢迎语"))
	if err != nil || text != "hello" {
		t.Fatalf("GetString() = %q, %v; want hello, nil", text, err)
	}
	ratio, err := logicObj.GetFloat(RequiredSysConfigKey("ratio", model.SysConfigTypeFloat, "比例"))
	if err != nil || ratio != 3.14 {
		t.Fatalf("GetFloat() = %v, %v; want 3.14, nil", ratio, err)
	}
	objectValue, err := logicObj.GetObject(RequiredSysConfigKey("objectValue", model.SysConfigTypeObject, "对象值"))
	if err != nil || !reflect.DeepEqual(objectValue, map[string]any{"a": json.Number("1")}) {
		t.Fatalf("GetObject() = %#v, %v", objectValue, err)
	}
	arrayValue, err := logicObj.GetArray(RequiredSysConfigKey("arrayValue", model.SysConfigTypeArray, "数组值"))
	if err != nil || !reflect.DeepEqual(arrayValue, []any{json.Number("1"), "b"}) {
		t.Fatalf("GetArray() = %#v, %v", arrayValue, err)
	}
}

// TestTypedSysConfigReturnsDefaultOnEmptyMarker 确保配置不存在时可返回声明默认值。
func TestTypedSysConfigReturnsDefaultOnEmptyMarker(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := newSysConfigLogicForKeyTest(client)
	cacheKey := logicObj.sysConfigCacheKey("featureFlag")
	if err := client.HSet(context.Background(), cacheKey, map[string]any{
		sysConfigCacheFieldValue: keys.EmptyValueMarker,
	}).Err(); err != nil {
		t.Fatalf("seed empty sys_config cache: %v", err)
	}

	flag, err := logicObj.GetBool(OptionalSysConfigKey("featureFlag", model.SysConfigTypeBoolean, false, "功能开关"))
	if err != nil {
		t.Fatalf("GetBool(default) error = %v", err)
	}
	if flag {
		t.Fatal("GetBool(default) = true, want false")
	}
}

// TestTypedSysConfigRejectsKeyTypeMismatch 确保调用方法和 key 声明类型不一致会返回错误。
func TestTypedSysConfigRejectsKeyTypeMismatch(t *testing.T) {
	logicObj := NewSysConfigLogic(context.Background(), svc.NewServiceContext(appconfig.Config{}, "v1", svc.Dependencies{}))
	if _, err := logicObj.GetBool(RequiredSysConfigKey("featureFlag", model.SysConfigTypeString, "功能开关")); err == nil {
		t.Fatal("expected key type mismatch error")
	}
}

// TestTypedSysConfigRejectsActualTypeMismatch 确保缓存实际类型不匹配会返回错误。
func TestTypedSysConfigRejectsActualTypeMismatch(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := newSysConfigLogicForKeyTest(client)
	seedTypedSysConfigCache(t, client, logicObj, "featureFlag", model.SysConfigTypeInteger, "1")
	if _, err := logicObj.GetBool(RequiredSysConfigKey("featureFlag", model.SysConfigTypeBoolean, "功能开关")); err == nil {
		t.Fatal("expected actual type mismatch error")
	}
}

// TestDecodeSysConfigValueRejectsNonCanonicalFormats 确保共享缓存只读取 Admin 当前写入的规范 JSON 形态。
func TestDecodeSysConfigValueRejectsNonCanonicalFormats(t *testing.T) {
	for _, raw := range []string{"true", "TRUE", "false", "2", "invalid", " 1 "} {
		if _, err := decodeSysConfigValue(model.SysConfigTypeBoolean, raw); err == nil {
			t.Fatalf("decodeSysConfigValue(boolean, %q) expected error", raw)
		}
	}
	if _, err := decodeSysConfigValue(model.SysConfigTypeString, "plain-text"); err == nil {
		t.Fatal("decodeSysConfigValue(string) expected malformed JSON error")
	}
}

// newSysConfigLogicForKeyTest 只注入 Redis，确保命中缓存的 Getter 不依赖数据库。
func newSysConfigLogicForKeyTest(client redis.UniversalClient) *SysConfigLogic {
	return NewSysConfigLogic(context.Background(), svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{Rds: client}))
}

// seedTypedSysConfigCache 按 Admin 写入的 Hash 字段预置共享缓存，不模拟数据库回源。
func seedTypedSysConfigCache(t *testing.T, client redis.UniversalClient, logicObj *SysConfigLogic, uuid string, typ int, value string) {
	t.Helper()
	cacheKey := logicObj.sysConfigCacheKey(uuid)
	if err := client.HSet(context.Background(), cacheKey, map[string]any{
		sysConfigCacheFieldType:  typ,
		sysConfigCacheFieldValue: value,
	}).Err(); err != nil {
		t.Fatalf("seed sys_config cache uuid=%s: %v", uuid, err)
	}
}
