package idgen

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// TestObservabilityDocumentsOnlyUseImplementedMetrics 防止监控资产引用不存在的业务指标。
func TestObservabilityDocumentsOnlyUseImplementedMetrics(t *testing.T) {
	t.Parallel()

	// 白名单只登记当前进程真实导出的业务指标。
	implemented := map[string]struct{}{
		"api_auth_rate_limit_cleanup_failures_total":           {},
		"api_collector_kafka_publish_events_total":             {},
		"api_idgen_generate_duration_seconds_bucket":           {},
		"api_idgen_generated_total":                            {},
		"api_idgen_segment_allocation_duration_seconds_bucket": {},
		"api_idgen_segment_allocations_total":                  {},
		"api_idgen_segment_remaining":                          {},
		"api_idgen_snowflake_lease_events_total":               {},
	}
	metricPattern := regexp.MustCompile(`\bapi_[a-z][a-z0-9_]*\b`)
	files := []string{
		"../../docs/grafana/api-dashboard.json",
		"../../docs/prometheus/api-alerts.yml",
	}

	// 监控资产中的 api_ 指标必须全部能映射到实现白名单。
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("读取监控资产 %s 失败: %v", file, err)
		}

		unknown := make(map[string]struct{})
		for _, metric := range metricPattern.FindAllString(string(content), -1) {
			if _, ok := implemented[metric]; !ok {
				unknown[metric] = struct{}{}
			}
		}
		if len(unknown) == 0 {
			continue
		}

		metrics := make([]string, 0, len(unknown))
		for metric := range unknown {
			metrics = append(metrics, metric)
		}
		sort.Strings(metrics)
		t.Fatalf("监控资产 %s 引用了未实现指标: %v", file, metrics)
	}
}
