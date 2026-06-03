package irc

import "log"

// Logger is the interface used by Client for logging. Compatible with *log.Logger.
type Logger interface {
	Printf(format string, v ...interface{})
}

// LoggerFunc is an adapter to allow the use of ordinary functions as Logger.
type LoggerFunc func(format string, v ...interface{})

func (f LoggerFunc) Printf(format string, v ...interface{}) {
	f(format, v...)
}

// defaultLogger returns a standard library logger with the "[xdcc] " prefix.
func defaultLogger() Logger {
	return log.New(log.Writer(), "[xdcc] ", log.Flags())
}
