package model

import (
	"api/internal/sharding"

	"github.com/Is999/go-utils/errors"
)

// UserPhysicalTableName 返回固定逻辑桶对应的用户表名。
func UserPhysicalTableName(shardNo int, routeShardCount int) (string, error) {
	plan, err := sharding.NewPlan(TableNameUser, routeShardCount)
	if err != nil {
		return "", errors.Tag(err)
	}
	table, err := plan.TableForBucket(shardNo)
	if err != nil {
		return "", errors.Tag(err)
	}
	return table.Name, nil
}
