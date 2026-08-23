package config

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	appconfig "api/internal/config"
	"api/internal/model"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestGetCachedValuePreservesContainerNumbers 覆盖真实缓存读取入口，容器内数字不能在重新编码时舍入。
func TestGetCachedValuePreservesContainerNumbers(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logicObj := newSysConfigLogicForKeyTest(client)
	for _, tc := range []struct {
		name string // 隔离对象、数组和声明为浮点数的缓存值。
		typ  int    // Admin 共享缓存声明类型，不改变 Float 的 float64 契约。
		raw  string // 同时覆盖大整数、嵌套负数、精确小数和指数。
	}{
		{name: "object", typ: model.SysConfigTypeObject, raw: `{"a":9007199254740993,"b":{"negative":-9007199254740993},"c":0.123456789012345678901,"d":1.234567890123456789e+20}`},
		{name: "array", typ: model.SysConfigTypeArray, raw: `[9007199254740993,{"negative":-9007199254740993},0.123456789012345678901,1.234567890123456789e+20]`},
		{name: "float", typ: model.SysConfigTypeFloat, raw: `3.14`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedTypedSysConfigCache(t, client, logicObj, tc.name, tc.typ, tc.raw)
			value, err := logicObj.GetCachedValue(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if tc.typ == model.SysConfigTypeFloat {
				if _, ok := value.(float64); !ok {
					t.Fatalf("Float 返回类型=%T，期望 float64", value)
				}
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.raw {
				t.Fatalf("缓存值被改写: got=%s want=%s", encoded, tc.raw)
			}
		})
	}
}

// TestDecodeSysConfigValue 校验系统配置缓存值按类型还原为业务值。
func TestDecodeSysConfigValue(t *testing.T) {
	tests := []struct {
		name string // 标明共享缓存中的数据类型
		typ  int    // Admin 写入的类型枚举，决定对应解码器
		raw  string // Redis Hash 中的 value 文本，而非 YAML 原值
		want any    // JSON 容器数字保留 json.Number，独立标量按声明类型解码。
	}{
		{name: "object", typ: model.SysConfigTypeObject, raw: `{"a":1}`, want: map[string]any{"a": json.Number("1")}},
		{name: "array", typ: model.SysConfigTypeArray, raw: `[1,"b"]`, want: []any{json.Number("1"), "b"}},
		{name: "string_json", typ: model.SysConfigTypeString, raw: `"hello"`, want: "hello"},
		{name: "integer", typ: model.SysConfigTypeInteger, raw: `42`, want: 42},
		{name: "float", typ: model.SysConfigTypeFloat, raw: `3.14`, want: 3.14},
		{name: "boolean", typ: model.SysConfigTypeBoolean, raw: `1`, want: true},
		{name: "group", typ: model.SysConfigTypeGroup, raw: `0`, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeSysConfigValue(tt.typ, tt.raw)
			if err != nil {
				t.Fatalf("decodeSysConfigValue() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeSysConfigValue() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestDecodeSysConfigValueRejectsUnknownType 防止损坏枚举被当作字符串兼容，隐藏数据与读取契约不一致。
func TestDecodeSysConfigValueRejectsUnknownType(t *testing.T) {
	if _, err := decodeSysConfigValue(7, "value"); err == nil {
		t.Fatal("decodeSysConfigValue() expected unknown type error")
	}
}

// TestDecodeSysConfigValueRejectsDeclaredShapeMismatch 确保类型枚举不能把合法但形状错误的 JSON 交给业务调用方。
func TestDecodeSysConfigValueRejectsDeclaredShapeMismatch(t *testing.T) {
	for _, test := range []struct {
		typ int    // typ 表示配置声明类型。
		raw string // raw 表示缓存中的 JSON 文本。
	}{
		{typ: model.SysConfigTypeObject, raw: `[]`},
		{typ: model.SysConfigTypeObject, raw: `null`},
		{typ: model.SysConfigTypeArray, raw: `{}`},
		{typ: model.SysConfigTypeArray, raw: `null`},
		{typ: model.SysConfigTypeObject, raw: `{"a":1} {}`},
		{typ: model.SysConfigTypeArray, raw: `[1] []`},
		{typ: model.SysConfigTypeObject, raw: `{"a":1} broken`},
		{typ: model.SysConfigTypeInteger, raw: `+1`},
		{typ: model.SysConfigTypeFloat, raw: `NaN`},
	} {
		if _, err := decodeSysConfigValue(test.typ, test.raw); err == nil {
			t.Fatalf("decodeSysConfigValue(type=%d, raw=%q) should fail", test.typ, test.raw)
		}
	}
}

// TestSysConfigCacheKeyUsesAppNamespace 校验系统配置缓存按 app_id 精确隔离。
func TestSysConfigCacheKeyUsesAppNamespace(t *testing.T) {
	logicObj := NewSysConfigLogic(context.Background(), svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{}))

	got := logicObj.sysConfigCacheKey("featureFlag")
	want := "app:site-a:table:config_uuid:featureFlag"
	if got != want {
		t.Fatalf("sysConfigCacheKey() = %q, want %q", got, want)
	}
}

// TestGetCachedValueReadsRedisBeforeDB 校验系统配置命中 Redis 后不会依赖数据库连接。
func TestGetCachedValueReadsRedisBeforeDB(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := NewSysConfigLogic(context.Background(), svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{Rds: client}))
	cacheKey := logicObj.sysConfigCacheKey("featureFlag")
	if err := client.HSet(context.Background(), cacheKey, map[string]any{
		sysConfigCacheFieldType:  model.SysConfigTypeBoolean,
		sysConfigCacheFieldValue: "1",
	}).Err(); err != nil {
		t.Fatalf("seed sys_config cache: %v", err)
	}

	value, err := logicObj.GetCachedValue("featureFlag")
	if err != nil {
		t.Fatalf("GetCachedValue() error = %v", err)
	}
	if value != true {
		t.Fatalf("GetCachedValue() = %#v, want true", value)
	}
}

// TestGetCachedValueRejectsInvalidCacheType 确保损坏缓存不会被静默当成 group 类型。
func TestGetCachedValueRejectsInvalidCacheType(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := NewSysConfigLogic(context.Background(), svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{Rds: client}))
	cacheKey := logicObj.sysConfigCacheKey("featureFlag")
	if err := client.HSet(context.Background(), cacheKey, map[string]any{
		sysConfigCacheFieldType:  "bad",
		sysConfigCacheFieldValue: "1",
	}).Err(); err != nil {
		t.Fatalf("seed sys_config cache: %v", err)
	}

	if _, err := logicObj.GetCachedValue("featureFlag"); err == nil {
		t.Fatal("GetCachedValue() expected invalid cache type error")
	}
}

// TestRuntimeRegistrySpecsValid 确保运行期配置注册入口规格完整且名称唯一。
func TestRuntimeRegistrySpecsValid(t *testing.T) {
	// 此处只检查清单元数据；是否完成实际注册由 bootstrap 启动测试覆盖。
	specs := RuntimeRegistrySpecs()
	if len(specs) == 0 {
		t.Fatal("RuntimeRegistrySpecs() 不能为空")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.Name == "" || spec.File == "" || spec.Method == "" || spec.Description == "" {
			t.Fatalf("运行时注册规格字段不完整: %+v", spec)
		}
		if _, ok := seen[spec.Name]; ok {
			t.Fatalf("运行时注册规格名称重复: %s", spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
}
