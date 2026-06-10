package logging

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var Log zerolog.Logger

func Init(pretty bool) {
	level := zerolog.InfoLevel
	if parsed, err := zerolog.ParseLevel(strings.TrimSpace(os.Getenv("LOG_LEVEL"))); err == nil {
		level = parsed
	}
	zerolog.SetGlobalLevel(level)

	if pretty {
		Log = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).With().Timestamp().Logger()
	} else {
		Log = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
	log.Logger = Log
}
