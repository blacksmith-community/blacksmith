package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

var Debugging bool
var Logger *Log

func init() {
	if os.Getenv("DEBUG") != "" || os.Getenv("BLACKSMITH_DEBUG") != "" {
		Debugging = true
	}
	Logger = &Log{
		max:   8192,
		index: 0,
		ring:  make([]string, 0),
	}
}

type Log struct {
	ctx []string
	up  *Log

	index int
	ring  []string
	max   int
}

func (l *Log) Full() bool {
	return len(l.ring) == l.max
}

func (l *Log) String() string {
	if l.ring == nil {
		return ""
	}

	if l.Full() {
		return strings.Join(l.ring[l.index:l.max], "") +
			strings.Join(l.ring[0:l.index], "")
	}
	return strings.Join(l.ring, "")
}

func (l *Log) Audit(msg string) {
	if l.up != nil {
		l.up.Audit(msg)
		return
	}
	if l.ring == nil {
		return
	}

	if l.Full() {
		l.ring[l.index] = msg
		l.index = (l.index + 1) % l.max
		return
	}
	l.ring = append(l.ring, msg)
}

func (l *Log) Wrap(f string, args ...interface{}) *Log {
	return &Log{up: l, ctx: append(l.ctx, fmt.Sprintf(f, args...))}
}

func (l *Log) printf(lvl, f string, args ...interface{}) {
	m := fmt.Sprintf(f, args...)
	now := time.Now().Format("2006-01-02 15:04:05.000")
	if len(l.ctx) == 0 {
		m = fmt.Sprintf("%s %-5s  %s\n", now, lvl, m)
	} else {
		m = fmt.Sprintf("%s %-5s  [%s] %s\n", now, lvl, strings.Join(l.ctx, " / "), m)
	}

	// Route debug logs to STDERR, info logs to STDOUT
	if lvl == "DEBUG" {
		fmt.Fprintf(os.Stderr, "%s", m)
	} else if lvl == "INFO" {
		fmt.Fprintf(os.Stdout, "%s", m)
	} else {
		// Error and other levels still go to STDERR
		fmt.Fprintf(os.Stderr, "%s", m)
	}
	l.Audit(m)
}

func (l *Log) Debug(f string, args ...interface{}) {
	if Debugging {
		l.printf("DEBUG", f, args...)
	}
}

func (l *Log) Info(f string, args ...interface{}) {
	l.printf("INFO", f, args...)
}

func (l *Log) Error(f string, args ...interface{}) {
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
