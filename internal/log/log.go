// Package log provides a single entry point for stdout logging.
//
// printLog renders lines as: [yyyy-mm-dd hh:ii:ss] [topic] message
package log

import (
	"fmt"
	"os"
	"sync"
	"time"
)

var mu sync.Mutex

// Print writes a single log line to stdout in the format
// [yyyy-mm-dd hh:ii:ss] [topic] message
func Print(topic, format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()
	ts := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stdout, "[%s] [%s] %s\n", ts, topic, msg)
}

// Println is a convenience wrapper that does not take a format string.
func Println(topic string, args ...any) {
	Print(topic, "%s", fmt.Sprintln(args...))
}
