package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestDashboardDurationUnits 验证耗时分位数以秒展示，避免把秒误标成每秒操作数。
func TestDashboardDurationUnits(t *testing.T) {
	content, err := os.ReadFile("grafana/api-dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			Title       string `json:"title"` // 失败时定位单位错误的面板
			FieldConfig struct {
				Defaults struct {
					Unit string `json:"unit"` // Grafana 的秒单位标识为 s
				} `json:"defaults"` // 面板所有序列共享的展示单位
			} `json:"fieldConfig"` // 不连接 Grafana，只验证发布 JSON 的显示契约
			Targets []struct {
				Expr string `json:"expr"` // 依据当前 PromQL 判断是否返回秒级分位数
			} `json:"targets"` // 不把计数速率面板误判为耗时
		} `json:"panels"` // 随发布包交付的全部面板
	}
	if err = json.Unmarshal(content, &dashboard); err != nil {
		t.Fatal(err)
	}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			if strings.HasPrefix(target.Expr, "histogram_quantile(") && strings.Contains(target.Expr, "_seconds_bucket") && panel.FieldConfig.Defaults.Unit != "s" {
				t.Errorf("面板 %q 单位为 %q，期望 s", panel.Title, panel.FieldConfig.Defaults.Unit)
			}
		}
	}
}
