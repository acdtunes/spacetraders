package persistence

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringToPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
