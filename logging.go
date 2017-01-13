package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

var Debugging bool
var Logger Log

func init() {
	if os.Getenv("DEBUG") != "" || os.Getenv("BLACKSMITH_DEBUG") != "" {
		Debugging = true
	}
}

type Log struct {
	Context []string
}

func (l Log) Wrap(f string, args ...interface{}) Log {
	return Log{append(l.Context, fmt.Sprintf(f, args...))}
}

func (l Log) printf(lvl, f string, args ...interface{}) {
	if len(l.Context) == 0 {
		fmt.Fprintf(os.Stderr, "%-40s %-5s  %s\n", time.Now(), lvl, fmt.Sprintf(f, args...))
	} else {
		fmt.Fprintf(os.Stderr, "%-40s %-5s  [%s] %s\n", time.Now(), lvl, strings.Join(l.Context, " / "), fmt.Sprintf(f, args...))
	}
}

func (l Log) Debug(f string, args ...interface{}) {
	if Debugging {
		l.printf("DEBUG", f, args...)
	}
}

func (l Log) Info(f string, args ...interface{}) {
	l.printf("INFO", f, args...)
}

func (l Log) Error(f string, args ...interface{}) {
	l.printf("ERROR", f, args...)
}

func Debug(f string, args ...interface{}) {
	Logger.Debug(f, args...)
}

func Info(f string, args ...interface{}) {
	Logger.Info(f, args...)
}

func Error(f string, args ...interface{}) {
	Logger.Error(f, args...)
}
