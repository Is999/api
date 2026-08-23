package validators

import (
	"strings"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
)

// API Collector Kafka Producer 配置上限。
const (
	maxCollectorKafkaWriteBatchSize             = 5000 // Producer 单批写入上限
	maxCollectorKafkaWriteBatchWaitMilliseconds = 5000 // 请求链路内 Producer 聚合等待上限，单位毫秒
	maxCollectorKafkaWriteTimeoutSeconds        = 30   // 请求链路内 Producer 写入超时上限，单位秒
)

// ValidateCollector 校验 API Collector Kafka 投递配置是否自洽。
func ValidateCollector(c config.Config) error {
	cfg := c.Collector
	if !cfg.Enabled {
		return nil
	}
	// Broker 地址必须非空且唯一，避免无效节点放大连接重试。
	if len(cfg.Kafka.Brokers) == 0 {
		return errors.Errorf("collector.enabled=true 时必须配置 collector.kafka.brokers")
	}
	brokers := make(map[string]struct{}, len(cfg.Kafka.Brokers))
	for index, broker := range cfg.Kafka.Brokers {
		if broker == "" || strings.TrimSpace(broker) != broker {
			return errors.Errorf("collector.kafka.brokers[%d] 不能为空或包含首尾空白", index)
		}
		if _, exists := brokers[broker]; exists {
			return errors.Errorf("collector.kafka.brokers 存在重复地址 %s", broker)
		}
		brokers[broker] = struct{}{}
	}
	// Producer 批量、等待和写超时均限制在请求链路可控范围内。
	if cfg.Kafka.WriteBatchSize < 0 || cfg.Kafka.WriteBatchSize > maxCollectorKafkaWriteBatchSize {
		return errors.Errorf("collector.kafka.write_batch_size 必须在 0-%d 之间", maxCollectorKafkaWriteBatchSize)
	}
	if cfg.Kafka.WriteBatchWaitMilliseconds < 0 || cfg.Kafka.WriteBatchWaitMilliseconds > maxCollectorKafkaWriteBatchWaitMilliseconds {
		return errors.Errorf("collector.kafka.write_batch_wait_milliseconds 必须在 0-%d 之间", maxCollectorKafkaWriteBatchWaitMilliseconds)
	}
	if cfg.Kafka.WriteTimeout < 0 || cfg.Kafka.WriteTimeout > maxCollectorKafkaWriteTimeoutSeconds {
		return errors.Errorf("collector.kafka.write_timeout 必须在 0-%d 之间", maxCollectorKafkaWriteTimeoutSeconds)
	}
	// 每个业务类型必须映射到明确 Topic，Topic 总数另受连接规模限制。
	if len(cfg.Tasks) == 0 {
		return errors.Errorf("collector.enabled=true 时必须配置 collector.tasks.<bizType>.topic")
	}
	if len(cfg.Tasks) > config.MaxCollectorTaskCount {
		return errors.Errorf("collector.tasks 不能超过 %d 个", config.MaxCollectorTaskCount)
	}
	topics := make(map[string]struct{}, len(cfg.Tasks))
	for bizType, task := range cfg.Tasks {
		if bizType == "" || strings.TrimSpace(bizType) != bizType {
			return errors.Errorf("collector.tasks 的 bizType 不能为空或包含首尾空白")
		}
		if task.Topic == "" || strings.TrimSpace(task.Topic) != task.Topic {
			return errors.Errorf("collector.tasks.%s.topic 不能为空或包含首尾空白", bizType)
		}
		topics[task.Topic] = struct{}{}
	}
	if len(topics) > config.MaxCollectorTopicCount {
		return errors.Errorf("collector.tasks 不同 topic 不能超过 %d 个", config.MaxCollectorTopicCount)
	}
	return nil
}
