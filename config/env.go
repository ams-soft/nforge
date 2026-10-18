package config

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func applyEnvFile(path string, cfg *Config) error {
	vars, err := godotenv.Read(path)
	if err != nil {
		return err
	}
	return applyEnvVars(reflect.ValueOf(cfg).Elem(), nil, vars)
}

func applyEnvVars(v reflect.Value, prefix []string, vars map[string]string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)

		name := yamlFieldName(field)
		path := append(append([]string{}, prefix...), name)

		if fv.Kind() == reflect.Struct {
			if err := applyEnvVars(fv, path, vars); err != nil {
				return err
			}
			continue
		}

		envKey := strings.ToUpper(strings.Join(path, "_"))
		raw, ok := vars[envKey]
		if !ok {
			continue // includes sequences of structs (jose.keys, jose.profiles): YAML only
		}
		if fv.Kind() == reflect.Map {
			return fmt.Errorf("field %s (env %s): maps cannot be set from the env file; use YAML", strings.Join(path, "."), envKey)
		}
		if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("field %s (env %s): sequences of %s cannot be set from the env file; use ${VAR} in YAML", strings.Join(path, "."), envKey, fv.Type().Elem())
		}
		if err := setFieldValue(fv, raw); err != nil {
			return fmt.Errorf("field %s (env %s): %w", strings.Join(path, "."), envKey, err)
		}
	}
	return nil
}

func yamlFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("yaml")
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		name = strings.ToLower(field.Name)
	}
	return name
}

func setFieldValue(fv reflect.Value, raw string) error {
	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(raw))
		}
	}
	if fv.Type() == reflect.TypeFor[time.Duration]() {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		if fv.OverflowInt(n) {
			return fmt.Errorf("value %d overflows %s", n, fv.Type())
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		if fv.OverflowUint(n) {
			return fmt.Errorf("value %d overflows %s", n, fv.Type())
		}
		fv.SetUint(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return err
		}
		fv.SetFloat(f)
	case reflect.Pointer:
		ptr := reflect.New(fv.Type().Elem())
		if err := setFieldValue(ptr.Elem(), raw); err != nil {
			return err
		}
		fv.Set(ptr)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("field kind not supported: %s", fv.Type())
		}
		var items []string
		for item := range strings.SplitSeq(raw, ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		fv.Set(reflect.ValueOf(items))
	default:
		return fmt.Errorf("field kind not supported: %s", fv.Kind())
	}
	return nil
}
