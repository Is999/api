// Package sharding 提供应用内固定逻辑桶到物理表的稳定路由。
package sharding

import (
	"fmt"
	"strings"

	"api/common/idgen"

	"github.com/Is999/go-utils/errors"
)

const (
	// BucketTotal 是所有 UID 业务表共享的固定逻辑桶数量。
	BucketTotal = idgen.ShardMod
	// maxTableNameLength 是 MySQL 标识符长度上限。
	maxTableNameLength = 64
)

// Table 表示一个物理表负责的固定桶闭区间。
type Table struct {
	Name        string // 物理表名
	Index       int    // 当前分片计划内的顺序
	BucketStart int    // 起始逻辑桶，包含
	BucketEnd   int    // 结束逻辑桶，包含
}

// Plan 表示一张 UID 业务表的物理拆分计划。
type Plan struct {
	firstTable string // 起始桶物理表，拆分时继续保留
	prefix     string // 新物理表名前缀
	count      int    // 当前物理表数量
}

// NewPlan 创建按固定逻辑桶等分的物理表计划。
func NewPlan(firstTable string, count int) (Plan, error) {
	firstTable = strings.TrimSpace(firstTable)
	if err := validateTableName(firstTable); err != nil {
		return Plan{}, errors.Tag(err)
	}
	if !ValidCount(count) {
		return Plan{}, errors.Errorf("物理分片数必须是 1..%d 内的 2 的幂 count=%d", BucketTotal, count)
	}
	prefix := strings.TrimSuffix(firstTable, "_0")
	if prefix == "" {
		return Plan{}, errors.Errorf("起始物理表名无有效前缀 table=%s", firstTable)
	}
	plan := Plan{firstTable: firstTable, prefix: prefix, count: count}
	// 构造时即验证最长生成表名，不能到第一次访问末分片时才发现标识符超限。
	if _, err := plan.TableAt(count - 1); err != nil {
		return Plan{}, errors.Tag(err)
	}
	return plan, nil
}

// ValidCount 判断物理分片数是否支持按固定桶平滑二分。
func ValidCount(count int) bool {
	return count > 0 && count <= BucketTotal && count&(count-1) == 0
}

// Index 返回固定逻辑桶所属的物理分片下标。
func (p Plan) Index(bucket int) (int, error) {
	if bucket < 0 || bucket >= BucketTotal {
		return 0, errors.Errorf("逻辑桶必须在 0..%d 之间 bucket=%d", BucketTotal-1, bucket)
	}
	return bucket * p.count / BucketTotal, nil
}

// TableForBucket 返回固定逻辑桶所属的物理表。
func (p Plan) TableForBucket(bucket int) (Table, error) {
	index, err := p.Index(bucket)
	if err != nil {
		return Table{}, errors.Tag(err)
	}
	return p.TableAt(index)
}

// TableAt 返回指定分片下标的物理表和桶范围。
func (p Plan) TableAt(index int) (Table, error) {
	if index < 0 || index >= p.count {
		return Table{}, errors.Errorf("物理分片下标越界 index=%d count=%d", index, p.count)
	}
	width := BucketTotal / p.count
	start := index * width
	name := p.firstTable
	if start > 0 {
		// 表名绑定固定桶起点，物理分片翻倍时保留已有表名。
		name = fmt.Sprintf("%s_b%04d", p.prefix, start)
	}
	if err := validateTableName(name); err != nil {
		return Table{}, errors.Tag(err)
	}
	return Table{
		Name:        name,
		Index:       index,
		BucketStart: start,
		BucketEnd:   start + width - 1,
	}, nil
}

// validateTableName 校验物理表标识符，禁止配置文本进入 SQL 结构。
func validateTableName(name string) error {
	if name == "" || len(name) > maxTableNameLength {
		return errors.Errorf("物理表名不能为空且长度不能超过 %d table=%s", maxTableNameLength, name)
	}
	for index, char := range name {
		if char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return errors.Errorf("物理表名包含非法字符 table=%s", name)
	}
	return nil
}
