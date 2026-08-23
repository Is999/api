package bootstrap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"api/common/runtimecfg"
	"api/internal/bootstrap/configload"
	"api/internal/config"
	"api/internal/security"
	"api/internal/svc"

	yaml "go.yaml.in/yaml/v2"
)

// TestConfigReloadKeepsVersionWithReadSnapshot 在真实加载返回后暂停发布，复现配置文件的普通原子替换。
func TestConfigReloadKeepsVersionWithReadSnapshot(t *testing.T) {
	for _, target := range []string{"main", "runtime"} {
		t.Run(target, func(t *testing.T) {
			cfg, _, _, err := LoadConfig("../../etc/config.dnmp.sample.yaml")
			if err != nil {
				t.Fatal(err)
			}
			// 密钥始终使用合法普通文件，时序屏障不借用非法文件类型。
			dir := t.TempDir()
			file := filepath.Join(dir, "config.yaml")
			keyFile := filepath.Join(dir, "key.txt")
			if err = os.WriteFile(keyFile, []byte("0123456789abcdef0123456789abcdef"), 0o600); err != nil {
				t.Fatal(err)
			}
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			publicKey, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
				SignStatus: 1, StableVersion: "v1",
				Versions: []config.SecuritySecretKeyVersionConfig{{
					KeyVersion: "v1", AESKeyRef: keyFile,
					RSAPublicKeyUser:    string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicKey})),
					RSAPrivateKeyServer: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
				}},
			}
			// 样例保留全部真实校验字段；测试只改变密码下限与密钥来源。
			sample, err := os.ReadFile("../../etc/config.dnmp.sample.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var raw any
			if err = yaml.Unmarshal(sample, &raw); err != nil {
				t.Fatal(err)
			}
			payload := snapshotTestYAML(raw).(map[string]any)
			securityJSON, err := json.Marshal(cfg.Security)
			if err != nil {
				t.Fatal(err)
			}
			var securityPayload map[string]any
			if err = json.Unmarshal(securityJSON, &securityPayload); err != nil {
				t.Fatal(err)
			}
			payload["security"] = securityPayload
			payload["hot_reload"] = map[string]any{"enabled": false}
			authPayload := payload["auth"].(map[string]any)
			authPayload["password_min_length"] = 8
			changedFile := file
			changedPayload := payload
			if target == "runtime" {
				changedFile = filepath.Join(dir, "runtime.yaml")
				payload["config_files"] = map[string]any{"runtime": "runtime.yaml"}
				changedPayload = map[string]any{"auth": authPayload}
			}
			writeSnapshotTestJSON(t, file, payload)
			if target == "runtime" {
				writeSnapshotTestJSON(t, changedFile, changedPayload)
			}
			authPayload["password_min_length"] = 12
			updated, err := json.Marshal(changedPayload)
			if err != nil {
				t.Fatal(err)
			}
			cfg.HotReload.Enabled = false
			previous := runtimecfg.Get()
			t.Cleanup(func() { runtimecfg.Restore(previous) })
			app := &App{ConfigFile: file, ServiceContext: svc.NewServiceContext(cfg, "initial", svc.Dependencies{})}
			// Load 完成所有真实解码和密钥校验后，固定这一轮结果再替换磁盘文件。
			loaded := make(chan string, 1)
			release := make(chan struct{})
			releaseLoad := sync.OnceFunc(func() { close(release) })
			load := func(path string) (config.Config, string, *security.KeyRegistry, error) {
				loadedConfig, fingerprint, registry, loadErr := configload.Load(path)
				if loadErr != nil {
					return loadedConfig, fingerprint, registry, loadErr
				}
				loaded <- fingerprint
				<-release
				return loadedConfig, fingerprint, registry, nil
			}
			reloaded := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, loadErr := app.reloadConfigFile(t.Context(), "manual", file, load)
				reloaded <- loadErr
			}()
			t.Cleanup(func() {
				releaseLoad()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Error("配置加载测试协程未结束")
				}
			})
			var firstFingerprint string // 屏障固定的旧内容指纹，不能被后续磁盘版本覆盖。
			select {
			case firstFingerprint = <-loaded:
			case loadErr := <-reloaded:
				t.Fatalf("配置未到达加载屏障: %v", loadErr)
			case <-time.After(5 * time.Second):
				t.Fatal("等待配置加载屏障超时")
			}
			if err = os.WriteFile(filepath.Join(dir, "next.yaml"), updated, 0o600); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(filepath.Join(dir, "next.yaml"), changedFile); err != nil {
				t.Fatal(err)
			}
			releaseLoad()
			select {
			case err = <-reloaded:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("加载屏障已释放，配置重载仍未结束")
			}
			if got := app.ServiceContext.CurrentConfig().Auth.PasswordMinLength; got != 8 {
				t.Fatalf("首次重载应发布已读取的旧密码下限，实际=%d", got)
			}
			if app.ServiceContext.CurrentVersion() != configload.Version(firstFingerprint) {
				t.Fatal("首次生效版本不属于本轮已读取快照")
			}
			// 第二轮真实加载必须应用新内容，不能因误记版本而跳过。
			fingerprint, err := app.reloadConfigFile(t.Context(), "manual", file, configload.Load)
			if err != nil {
				t.Fatal(err)
			}
			if got := app.ServiceContext.CurrentConfig().Auth.PasswordMinLength; got != 12 {
				t.Fatalf("两次成功重载后仍为旧密码下限=%d", got)
			}
			if app.ServiceContext.CurrentVersion() != configload.Version(fingerprint) {
				t.Fatal("生效版本和成功返回的指纹不是同一快照")
			}
			if fingerprint == firstFingerprint {
				t.Fatal("普通文件原子替换后仍返回旧内容指纹")
			}
		})
	}
}

// writeSnapshotTestJSON 写出完整测试配置，JSON 是 YAML 的子集，仍走真实 YAML 校验。
func writeSnapshotTestJSON(t *testing.T, file string, payload map[string]any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// snapshotTestYAML 仅转换测试样例动态映射的键形状，方便不丢字段地生成 JSON fixture。
func snapshotTestYAML(value any) any {
	switch typed := value.(type) {
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key.(string)] = snapshotTestYAML(item)
		}
		return result
	case []any:
		for index := range typed {
			typed[index] = snapshotTestYAML(typed[index])
		}
		return typed
	default:
		return value
	}
}
