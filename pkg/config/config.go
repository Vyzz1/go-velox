package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"

	"github.com/joho/godotenv"
)

// Base holds fields common to every service.
// Embed this in each service-specific config struct.
type Base struct {
	ServiceName string `env:"SERVICE_NAME" envDefault:"unknown"`
	Environment string `env:"ENVIRONMENT"  envDefault:"development"`
	LogLevel    string `env:"LOG_LEVEL"    envDefault:"info"`
	LogFormat   string `env:"LOG_FORMAT"   envDefault:"json"`
}

// Load reads the given .env files in order (later files win on duplicate keys),
// then maps environment variables into cfg using `env` and `envDefault` struct
// tags. cfg must be a non-nil pointer to a struct.
// If no files are given it falls back to a root-level ".env".
func Load(cfg any, envFiles ...string) error {
	if len(envFiles) == 0 {
		envFiles = []string{".env"}
	}
	_ = godotenv.Load(envFiles...) // best-effort; missing files are not an error
	return parseStruct(reflect.ValueOf(cfg))
}

func parseStruct(v reflect.Value) error {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("config: expected pointer to struct, got %s", v.Kind())
	}
	t := v.Type()
	for i := range t.NumField() {
		field := t.Field(i)
		fv := v.Field(i)

		if !fv.CanSet() {
			continue
		}

		// recurse into embedded structs (e.g. embedded Base)
		if field.Anonymous && fv.Kind() == reflect.Struct {
			if err := parseStruct(fv); err != nil {
				return err
			}
			continue
		}

		key, ok := field.Tag.Lookup("env")
		if !ok {
			continue
		}

		raw, found := os.LookupEnv(key)
		if !found || raw == "" {
			raw = field.Tag.Get("envDefault")
		}

		if err := setField(fv, raw); err != nil {
			return fmt.Errorf("config: field %s (%s): %w", field.Name, key, err)
		}
	}
	return nil
}

func setField(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		if raw == "" {
			return nil
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if raw == "" {
			return nil
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	}
	return nil
}
