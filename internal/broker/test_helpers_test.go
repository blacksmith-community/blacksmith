package broker_test

func toInterfaceSlice(values []interface{}) []interface{} {
	out := make([]interface{}, len(values))
	for i, value := range values {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[i] = toInterfaceMap(typed)
		case []interface{}:
			out[i] = toInterfaceSlice(typed)
		default:
			out[i] = typed
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
			for i, v := range typed {
				converted[i] = v
			}

			out[key] = converted
		default:
			out[key] = typed
		}
	}

	return out
}
