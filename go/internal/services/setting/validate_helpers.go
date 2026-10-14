// validate_helpers.go: scalar field validators shared by the settings and
// patch schemas (bool/string/enum/int shapes from validationAppSettings.ts).

package setting

func validateBool(v any) error {
	_, ok := v.(bool)
	if !ok {
		return errType
	}
	return nil
}

func isString(v any) bool { _, ok := v.(string); return ok }

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && trimSpace(s) != ""
}

func nonEmptyStringField(m map[string]any, key string) bool {
	value, ok := m[key]
	return ok && nonEmptyString(value)
}

func validateNonEmptyTrimmedString(v any) error {
	s, ok := v.(string)
	if !ok {
		return errType
	}
	if trimSpace(s) == "" {
		return errRange
	}
	return nil
}

func validateStringArray(v any) error {
	arr, ok := v.([]any)
	if !ok {
		return errType
	}
	for _, item := range arr {
		if !isString(item) {
			return errType
		}
	}
	return nil
}

func validateStringRecord(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	for _, item := range obj {
		if !isString(item) {
			return errType
		}
	}
	return nil
}

func validateShortcutBindings(v any) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return errType
	}
	for _, item := range obj {
		if err := validateStringArray(item); err != nil {
			return err
		}
	}
	return nil
}

func validateLocale(v any) error {
	return enumValidator("zh-CN", "en-US")(v)
}

func validateLocalePreference(v any) error {
	return enumValidator("system", "zh-CN", "en-US")(v)
}

func validateOccupation(v any) error {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || !occupationSet[s] {
		return errEnum
	}
	return nil
}

func enumValidator(values ...string) func(any) error {
	set := map[string]bool{}
	for _, v := range values {
		set[v] = true
	}
	return func(v any) error {
		s, ok := v.(string)
		if !ok || !set[s] {
			return errEnum
		}
		return nil
	}
}
