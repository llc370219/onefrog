package module

import "onefrog/internal/model"

func Catalog() []model.Module {
	return []model.Module{
		{ID: "probe", Name: "服务器探针", Version: "1.0.0", Type: "builtin", Description: "每台机器的在线、磁盘和负载", Enabled: true},
		{ID: "services", Name: "服务管理", Version: "1.1.0", Type: "builtin", Description: "按服务器检测 systemd 服务、Docker 容器与监听端口，联动 AI 检查", Enabled: true},
		{ID: "alerts", Name: "需要处理", Version: "1.0.0", Type: "builtin", Description: "失联和被拦住的高危操作", Enabled: true},
		{ID: "compare", Name: "磁盘对比", Version: "1.0.0", Type: "builtin", Description: "把几台机器的磁盘放在一起看", Enabled: false},
		{ID: "assets", Name: "订阅", Version: "1.1.0", Type: "builtin", Description: "统一记录服务器、域名和软件订阅，汇总月费、7 天内到期与历史订阅", Enabled: true},
		{ID: "develop", Name: "自定义模块", Version: "1.0.0", Type: "builtin", Description: "通过对话或源码创建可交互的自定义模块，挂载到面板与模块目录", Enabled: true},
	}
}

func Find(id string) (model.Module, bool) {
	if id == "资产与订阅" {
		id = "assets"
	}
	for _, item := range Catalog() {
		if item.ID == id || item.Name == id {
			return item, true
		}
	}
	return model.Module{}, false
}
