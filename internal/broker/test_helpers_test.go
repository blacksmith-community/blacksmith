package broker_test

func toInterfaceSlice(values []interface{}) []interface{} {
	out := make([]interface{}, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[index] = toInterfaceMap(typed)
		case []interface{}:
			out[index] = toInterfaceSlice(typed)
		default:
			out[index] = typed
		}
	}

	return out
}

func toInterfaceMap(values map[string]interface{}) map[interface{}]interface{} {
	out := make(map[interface{}]interface{}, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = toInterfaceMap(typed)
		case []interface{}:
			out[key] = toInterfaceSlice(typed)
		case []string:
			converted := make([]interface{}, len(typed))
			for idx, v := range typed {
				converted[idx] = v
			}

			out[key] = converted
		default:
			out[key] = typed
		}
	}

	return out
}
