package logic

// RuntimeRegistrySpec 供启动清单展示扩展来源，不通过文件名或方法名动态调用注册逻辑。
type RuntimeRegistrySpec struct {
	Name        string // 注册名称，必须在运行时扩展清单中唯一
	File        string // 注册实现所在文件
	Method      string // 注册入口方法
	Description string // 注册项中文说明
}
