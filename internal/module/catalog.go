package module

import "onefrog/internal/model"

func Catalog() []model.Module {
	return []model.Module{
		{ID: "probe", Name: "服务器探针", Version: "1.0.0", Type: "builtin", Description: "每台机器的在线、磁盘和负载", Enabled: true},
		{ID: "services", Name: "服务状态", Version: "1.0.0", Type: "builtin", Description: "nginx、数据库、缓存是否在跑", Enabled: true},
		{ID: "alerts", Name: "需要处理", Version: "1.0.0", Type: "builtin", Description: "失联和被拦住的高危操作", Enabled: true},
		{ID: "activity", Name: "最近操作", Version: "1.0.0", Type: "builtin", Description: "Bot 刚刚在哪台机器上做了什么", Enabled: true},
		{ID: "compare", Name: "磁盘对比", Version: "1.0.0", Type: "builtin", Description: "把几台机器的磁盘放在一起看", Enabled: false},
		{ID: "assets", Name: "资产与订阅", Version: "1.0.0", Type: "builtin", Description: "记录服务器资费、剩余时间、剩余价值和提供商", Enabled: true},
	}
}

func Find(id string) (model.Module, bool) {
	for _, item := range Catalog() {
		if item.ID == id || item.Name == id {
			return item, true
		}
	}
	return model.Module{}, false
}
