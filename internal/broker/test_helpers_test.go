package broker_test

func toPlanSlice(values []interface{}) []interface{} {
	out := make([]interface{}, len(values))
	for index, value := range values {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[index] = toPlanMap(typed)
		case []interface{}:
			out[index] = toPlanSlice(typed)
		default:
			out[index] = typed
		}
	}

	return out
}

// toPlanMap copies values into the form a plan's manifest and credentials
// take once the catalog loads them, with every list a []interface{}.
func toPlanMap(values map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case map[string]interface{}:
			out[key] = toPlanMap(typed)
		case []interface{}:
			out[key] = toPlanSlice(typed)
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
