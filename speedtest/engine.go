package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// proxyCore 表示单次测速任务实际使用的代理内核。
type proxyCore string

const (
	coreMihomo  proxyCore = "mihomo"
	coreSingBox proxyCore = "sing-box"
)

// proxyRuntime 绑定一次测速任务所选内核及其可执行文件。
type proxyRuntime struct {
	Core proxyCore
	Bin  string
}

// parseClashProxy 解析主控下发的单节点 Clash JSON，并保留整数精度。
func parseClashProxy(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var proxy map[string]any
	if err := decoder.Decode(&proxy); err != nil {
		return nil, fmt.Errorf("解析节点 Clash 配置失败: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("节点 Clash 配置包含多余内容")
	}
	if proxy == nil {
		return nil, fmt.Errorf("节点 Clash 配置不能为空")
	}
	return proxy, nil
}

// selectProxyCore 仅把 Snell v6 分流给 sing-box，其余配置继续交给 Mihomo。
func selectProxyCore(proxy map[string]any) (proxyCore, error) {
	proxyType := strings.ToLower(strings.TrimSpace(stringValue(proxy["type"])))
	if proxyType != "snell" {
		return coreMihomo, nil
	}
	versionRaw, exists := proxy["version"]
	if !exists || versionRaw == nil {
		return coreMihomo, nil
	}
	if versionText, isString := versionRaw.(string); isString && strings.TrimSpace(versionText) == "" {
		return coreMihomo, nil
	}
	version, err := integerValue(versionRaw)
	if err != nil {
		return "", fmt.Errorf("Snell version 必须是整数")
	}
	if version == 6 {
		return coreSingBox, nil
	}
	return coreMihomo, nil
}

// selectProxyCoreJSON 从主控原始配置中选择代理内核。
func selectProxyCoreJSON(raw string) (proxyCore, error) {
	proxy, err := parseClashProxy(raw)
	if err != nil {
		return "", err
	}
	return selectProxyCore(proxy)
}

// resolveProxyRuntime 只定位本次任务需要的内核，Snell v6 不再依赖 Mihomo 预热结果。
func resolveProxyRuntime(ctx context.Context, raw string) (proxyRuntime, error) {
	core, err := selectProxyCoreJSON(raw)
	if err != nil {
		return proxyRuntime{}, err
	}
	switch core {
	case coreSingBox:
		bin, ensureErr := EnsureSingBox(ctx)
		if ensureErr != nil {
			return proxyRuntime{}, fmt.Errorf("sing-box 不可用: %w", ensureErr)
		}
		return proxyRuntime{Core: core, Bin: bin}, nil
	case coreMihomo:
		bin, ensureErr := EnsureMihomo(ctx)
		if ensureErr != nil {
			return proxyRuntime{}, fmt.Errorf("mihomo 不可用: %w", ensureErr)
		}
		return proxyRuntime{Core: core, Bin: bin}, nil
	default:
		return proxyRuntime{}, fmt.Errorf("未知代理内核: %s", core)
	}
}

// integerValue 将 JSON 数字或数字字符串安全转换为整数。
func integerValue(value any) (int, error) {
	switch current := value.(type) {
	case json.Number:
		parsedInteger, integerErr := strconv.ParseInt(current.String(), 10, 64)
		if integerErr == nil {
			return int64ToInt(parsedInteger)
		}
		parsedFloat, floatErr := strconv.ParseFloat(current.String(), 64)
		if floatErr != nil {
			return 0, floatErr
		}
		return float64ToInt(parsedFloat)
	case string:
		trimmed := strings.TrimSpace(current)
		parsedInteger, integerErr := strconv.ParseInt(trimmed, 10, 64)
		if integerErr == nil {
			return int64ToInt(parsedInteger)
		}
		parsedFloat, floatErr := strconv.ParseFloat(trimmed, 64)
		if floatErr != nil {
			return 0, floatErr
		}
		return float64ToInt(parsedFloat)
	case float64:
		return float64ToInt(current)
	case float32:
		return float64ToInt(float64(current))
	case int:
		return current, nil
	case int8:
		return int(current), nil
	case int16:
		return int(current), nil
	case int32:
		return int(current), nil
	case int64:
		return int64ToInt(current)
	case uint:
		return uint64ToInt(uint64(current))
	case uint8:
		return int(current), nil
	case uint16:
		return int(current), nil
	case uint32:
		return uint64ToInt(uint64(current))
	case uint64:
		return uint64ToInt(current)
	default:
		return 0, fmt.Errorf("不是有效整数")
	}
}

// float64ToInt 只接受有限、无小数且落在当前平台 int 范围内的数值。
func float64ToInt(value float64) (int, error) {
	limit := math.Ldexp(1, strconv.IntSize-1)
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < -limit || value >= limit {
		return 0, fmt.Errorf("不是有效整数")
	}
	return int(value), nil
}

// int64ToInt 检查 32 位构建下的窄化溢出。
func int64ToInt(value int64) (int, error) {
	if strconv.IntSize == 32 && (value < math.MinInt32 || value > math.MaxInt32) {
		return 0, fmt.Errorf("整数超出范围")
	}
	return int(value), nil
}

// uint64ToInt 检查无符号整数是否超过当前平台最大 int。
func uint64ToInt(value uint64) (int, error) {
	limit := uint64(1) << (strconv.IntSize - 1)
	if value >= limit {
		return 0, fmt.Errorf("整数超出范围")
	}
	return int(value), nil
}

// normalizeJSONNumbers 在交给 Mihomo 前恢复 json.Unmarshal 原有的数字语义，避免 YAML 把 json.Number 写成字符串。
func normalizeJSONNumbers(value any) any {
	switch current := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(current.String(), 10, 64); err == nil {
			return integer
		}
		if decimal, err := strconv.ParseFloat(current.String(), 64); err == nil {
			return decimal
		}
		return current.String()
	case map[string]any:
		normalized := make(map[string]any, len(current))
		for key, child := range current {
			normalized[key] = normalizeJSONNumbers(child)
		}
		return normalized
	case []any:
		normalized := make([]any, len(current))
		for index, child := range current {
			normalized[index] = normalizeJSONNumbers(child)
		}
		return normalized
	default:
		return value
	}
}

// stringValue 只读取字符串，避免把复杂结构意外写入配置或错误日志。
func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

// boolValue 兼容常见的布尔值序列化形式。
func boolValue(value any) bool {
	switch current := value.(type) {
	case bool:
		return current
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(current))
		return err == nil && parsed
	case json.Number:
		return current.String() == "1"
	case float64:
		return current == 1
	case int:
		return current == 1
	default:
		return false
	}
}
