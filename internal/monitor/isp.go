package monitor

// Three-network (电信 / 联通 / 移动) latency targets per province.
//
// The hosts are public IPv4 TCP-Ping endpoints on CDN nodes inside each
// carrier's network in that province, named <province>-<ct|cu|cm>-v4.ip.zstaticcdn.com.
// Many of them drop ICMP, so the probe measures them with a TCP handshake to
// port 80 instead of ping.

// Province is a mainland China province-level region with its target code.
type Province struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// PingTarget is one line-quality target written into the probe config.
// Port > 0 means a TCP-Ping to host:port; 0 means ICMP ping.
type PingTarget struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

// Provinces lists the regions in a stable order (municipalities first, then by region).
var Provinces = []Province{
	{"bj", "北京"}, {"sh", "上海"}, {"tj", "天津"}, {"cq", "重庆"},
	{"gd", "广东"}, {"gx", "广西"}, {"hi", "海南"},
	{"js", "江苏"}, {"zj", "浙江"}, {"ah", "安徽"}, {"fj", "福建"}, {"jx", "江西"}, {"sd", "山东"},
	{"he", "河北"}, {"sx", "山西"}, {"nm", "内蒙古"},
	{"ln", "辽宁"}, {"jl", "吉林"}, {"hl", "黑龙江"},
	{"ha", "河南"}, {"hb", "湖北"}, {"hn", "湖南"},
	{"sc", "四川"}, {"gz", "贵州"}, {"yn", "云南"}, {"xz", "西藏"},
	{"sn", "陕西"}, {"gs", "甘肃"}, {"qh", "青海"}, {"nx", "宁夏"}, {"xj", "新疆"},
}

// Carriers in display order.
var carriers = []struct{ code, name string }{{"ct", "电信"}, {"cu", "联通"}, {"cm", "移动"}}

// LookupProvince accepts a code ("gd") or a name ("广东", "广东省").
func LookupProvince(v string) (Province, bool) {
	for _, p := range Provinces {
		if v == p.Code || v == p.Name || v == p.Name+"省" || v == p.Name+"市" {
			return p, true
		}
	}
	switch v { // full official names of the autonomous regions
	case "内蒙古自治区":
		return LookupProvince("nm")
	case "广西壮族自治区":
		return LookupProvince("gx")
	case "西藏自治区":
		return LookupProvince("xz")
	case "宁夏回族自治区":
		return LookupProvince("nx")
	case "新疆维吾尔自治区":
		return LookupProvince("xj")
	}
	return Province{}, false
}

// ISPTargets returns the 电信 / 联通 / 移动 targets for a province.
func ISPTargets(p Province) []PingTarget {
	out := make([]PingTarget, 0, len(carriers))
	for _, c := range carriers {
		out = append(out, PingTarget{Name: c.name, Host: p.Code + "-" + c.code + "-v4.ip.zstaticcdn.com", Port: 80})
	}
	return out
}
