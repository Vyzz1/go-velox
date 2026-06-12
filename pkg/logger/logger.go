package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds a zap.Logger from a level string ("debug","info","warn","error")
// and a format string ("json" or "console").
func New(level, format string) (*zap.Logger, error) {
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = zapcore.InfoLevel
	}

	var cfg zap.Config
	if format == "console" {
		cfg = zap.NewDevelopmentConfig()
	} else {
		cfg = zap.NewProductionConfig()
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	return cfg.Build()
}

// Must panics if the logger cannot be constructed.
func Must(level, format string) *zap.Logger {
	l, err := New(level, format)
	if err != nil {
		panic(err)
	}
	return l
}
