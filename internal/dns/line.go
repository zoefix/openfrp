package dns

import "sort"

type Line string

const (
	LineDefault Line = "DEF"

	LineTelecom Line = "CT"

	LineUnicom Line = "CU"

	LineMobile Line = "CM"

	LineOverseas Line = "AB"
)

var AllLines = []Line{LineDefault, LineTelecom, LineUnicom, LineMobile, LineOverseas}

func (l Line) Label() string {
	switch l {
	case LineDefault, "":
		return "Default"
	case LineTelecom:
		return "China Telecom"
	case LineUnicom:
		return "China Unicom"
	case LineMobile:
		return "China Mobile"
	case LineOverseas:
		return "Overseas"
	default:
		return string(l)
	}
}

var lineTables = map[string]map[Line]string{
	"aliyun": {
		LineDefault: "default", LineTelecom: "telecom", LineUnicom: "unicom",
		LineMobile: "mobile", LineOverseas: "oversea",
	},
	"dnspod": {
		LineDefault: "0", LineTelecom: "10=0", LineUnicom: "10=1",
		LineMobile: "10=3", LineOverseas: "3=0",
	},
	"huawei": {
		LineDefault: "default_view", LineTelecom: "Dianxin", LineUnicom: "Liantong",
		LineMobile: "Yidong", LineOverseas: "Abroad",
	},
	"west": {
		LineDefault: "", LineTelecom: "LTEL", LineUnicom: "LCNC",
		LineMobile: "LMOB", LineOverseas: "LFOR",
	},
	"baidu": {
		LineDefault: "default", LineTelecom: "ct", LineUnicom: "cnc",
		LineMobile: "cmnet",
	},
	"jdcloud": {
		LineDefault: "-1", LineTelecom: "1", LineUnicom: "2",
		LineMobile: "3", LineOverseas: "4",
	},
	"huoshan": {
		LineDefault: "default", LineTelecom: "telecom", LineUnicom: "unicom",
		LineMobile: "mobile", LineOverseas: "oversea",
	},
	"qingcloud": {
		LineDefault: "0", LineTelecom: "2", LineUnicom: "3",
		LineMobile: "4", LineOverseas: "8",
	},
	"bt": {
		LineDefault: "0", LineTelecom: "285344768", LineUnicom: "285345792",
		LineMobile: "285346816",
	},
	"dnsla": {
		LineDefault: "", LineTelecom: "84613316902921216",
		LineUnicom: "84613316923892736", LineMobile: "84613316953252864",
	},

	"cloudflare": {LineDefault: "0"},
	"namesilo":   {LineDefault: "default"},
	"henet":      {LineDefault: "default"},
	"powerdns":   {LineDefault: "default"},
	"spaceship":  {LineDefault: "default"},
	"technitium": {LineDefault: "default"},
	"aliyunesa":  {LineDefault: "0"},
	"tencenteo":  {LineDefault: "Default"},
}

func ProviderLine(provider string, line Line) (string, bool) {
	table, known := lineTables[provider]
	if !known {
		return "", false
	}
	if line == "" {
		line = LineDefault
	}
	value, ok := table[line]
	return value, ok
}

func LineFromProvider(provider, value string) Line {
	table, known := lineTables[provider]
	if !known {
		return LineDefault
	}
	for line, candidate := range table {
		if candidate == value {
			return line
		}
	}
	return LineDefault
}

func SupportedLines(provider string) []Line {
	table, known := lineTables[provider]
	if !known {
		return []Line{LineDefault}
	}

	var lines []Line
	for _, line := range AllLines {
		if _, ok := table[line]; ok {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = []Line{LineDefault}
	}
	return lines
}

func KnownProviderTables() []string {
	names := make([]string, 0, len(lineTables))
	for name := range lineTables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
