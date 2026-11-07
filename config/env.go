package config

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// readEnvFile parses an env file with every value taken literally.
// godotenv expands $NAME and ${NAME} in unquoted and double-quoted values from
// the keys defined earlier in the file (an undefined name becomes empty), so a
// password such as "pa$SWORD" would silently lose a piece. Hiding each "$"
// from the parser and restoring it afterwards makes "$" an ordinary character
// in every kind of value.
func readEnvFile(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxConfigFileBytes {
		return nil, fmt.Errorf("the file is larger than %d bytes", maxConfigFileBytes)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the operator's configuration
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, errors.New("the file contains a NUL byte")
	}
	parsed, err := godotenv.Unmarshal(string(bytes.ReplaceAll(raw, []byte("$"), []byte{0})))
	if err != nil {
		return nil, err
	}
	vars := make(map[string]string, len(parsed))
	for key, value := range parsed {
		vars[strings.ReplaceAll(key, "\x00", "$")] = strings.ReplaceAll(value, "\x00", "$")
	}
	return vars, nil
}

// applyEnvVars sets the fields of dst (a pointer to a struct) from the env
// file keys of section: the YAML path of each leaf in upper case, joined by
// "_" (http.server.port is HTTP_SERVER_PORT). The keys it consumed are added
// to used; the caller reports the keys nothing consumed. A dst that is not a
// pointer to a struct has no env keys.
func applyEnvVars(dst any, section string, vars map[string]string, used map[string]bool) error {
	if len(vars) == 0 {
		return nil
	}
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	return applyEnvFields(v.Elem(), []string{section}, vars, used)
}

func applyEnvFields(v reflect.Value, prefix []string, vars map[string]string, used map[string]bool) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)
		if !field.IsExported() || field.Tag.Get("yaml") == "-" {
			continue // not configuration (load-time values such as OnlineSince, Warnings)
		}

		name := yamlFieldName(field)
		path := append(append([]string{}, prefix...), name)

		if fv.Kind() == reflect.Struct {
			if err := applyEnvFields(fv, path, vars, used); err != nil {
				return err
			}
			continue
		}

		envKey := strings.ToUpper(strings.Join(path, "_"))
		raw, ok := vars[envKey]
		if !ok {
			continue // includes sequences of structs (jose.keys, jose.profiles): YAML only
		}
		used[envKey] = true
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
