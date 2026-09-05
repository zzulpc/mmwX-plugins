// Package valueutil 提供根解析包与 substore 共用的弱类型取值规则。
package valueutil

import (
	"net"
	"strings"
)

// Truthy 判断弱类型值是否明确表示真。
// 字符串仅接受常见布尔真值，避免把 "false"、"0" 或任意文本误判为真。
func Truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0
	case float64:
		return v != 0
	default:
		return false
	}
}

// FirstString 返回字符串或数组中的第一个非空字符串。
// 不把数字等其它标量格式化成文本，避免畸形输入成为 Host、路径或 ALPN。
func FirstString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []string:
		for _, item := range v {
			if item != "" {
				return item
			}
		}
	case []any:
		for _, item := range v {
			if text := FirstString(item); text != "" {
				return text
			}
		}
	}
	return ""
}

// IsIPv4 判断文本是否是 IPv4 地址，IPv4 映射地址也归入 IPv4。
func IsIPv4(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && ip.To4() != nil
}

// IsIPv6 判断文本是否是 IPv6 地址，并排除 IPv4 映射地址。
func IsIPv6(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && ip.To4() == nil
}
