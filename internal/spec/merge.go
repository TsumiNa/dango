package spec

import "fmt"

// MergeToolSpec overlays override values onto a base tool spec using recursive
// map replacement semantics.
func MergeToolSpec(base ToolSpec, override map[string]any) (ToolSpec, error) {
	baseMap, err := base.ToMap()
	if err != nil {
		return ToolSpec{}, err
	}

	merged := mergeMaps(baseMap, override)
	out, err := toolSpecFromMap(merged)
	if err != nil {
		return ToolSpec{}, err
	}

	if err := out.Validate(); err != nil {
		return ToolSpec{}, fmt.Errorf("validate merged tool spec: %w", err)
	}

	return out, nil
}

func mergeMaps(base, override map[string]any) map[string]any {
	result := cloneMap(base)
	for key, overrideValue := range override {
		baseValue, exists := result[key]
		baseMap, baseIsMap := asStringMap(baseValue)
		overrideMap, overrideIsMap := asStringMap(overrideValue)

		if exists && baseIsMap && overrideIsMap {
			result[key] = mergeMaps(baseMap, overrideMap)
			continue
		}

		result[key] = cloneValue(overrideValue)
	}

	return result
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}

	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = cloneValue(item)
	}

	return out
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneValue(item)
		}
		return out
	default:
		return typed
	}
}

func asStringMap(value any) (map[string]any, bool) {
	typed, ok := value.(map[string]any)
	if ok {
		return typed, true
	}

	return nil, false
}
