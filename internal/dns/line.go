package dns

import "sort"

// Line is a carrier split — an answer served only to resolvers on a particular
// network.
//
// Split-horizon answers by carrier are routine in China, where routing between
// China Telecom, Unicom and Mobile is poor enough that operators point each at
// a different address. Every provider supports it; no two agree on how to name
// the lines. This abstract code is what the UI and the config speak, and each
// provider maps it to whatever its API wants.
//
// The mapping tables below are ported from netcccyun/dnsmgr, which is the best
// curated collection of these values in existence. They are facts about other
// people's APIs, not design decisions, and getting one wrong produces a record
// that silently answers the wrong audience.
type Line string

const (
	// LineDefault answers everyone not matched by a more specific line.
	LineDefault Line = "DEF"
	// LineTelecom is China Telecom.
	LineTelecom Line = "CT"
	// LineUnicom is China Unicom.
	LineUnicom Line = "CU"
	// LineMobile is China Mobile.
	LineMobile Line = "CM"
	// LineOverseas is everything outside mainland China.
	LineOverseas Line = "AB"
)

// AllLines lists the abstract lines in display order.
var AllLines = []Line{LineDefault, LineTelecom, LineUnicom, LineMobile, LineOverseas}

// Label returns a human name for a line.
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

// lineTables maps a provider key onto its own line identifiers.
//
// A line absent from a provider's table is one that provider does not offer;
// callers must treat a missing entry as unsupported rather than substituting
// the default, because silently serving the default to a carrier the operator
// meant to split off is the opposite of what they asked for.
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
	// Providers with no carrier concept at all.
	"cloudflare": {LineDefault: "0"},
	"namesilo":   {LineDefault: "default"},
	"henet":      {LineDefault: "default"},
	"powerdns":   {LineDefault: "default"},
	"spaceship":  {LineDefault: "default"},
	"technitium": {LineDefault: "default"},
	"aliyunesa":  {LineDefault: "0"},
	"tencenteo":  {LineDefault: "Default"},
}

// ProviderLine maps an abstract line onto a provider's own identifier.
//
// An empty line is treated as the default. The second return value is false
// when the provider does not offer that line at all.
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

// LineFromProvider maps a provider's identifier back to an abstract line.
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

// SupportedLines lists the lines a provider offers, in display order.
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

// KnownProviderTables lists the providers with a line mapping, for tests.
func KnownProviderTables() []string {
	names := make([]string, 0, len(lineTables))
	for name := range lineTables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
