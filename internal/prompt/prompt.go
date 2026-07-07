// Package prompt loads prompt template files and populates variables.
//
// Supported variables:
//   - {user_description}
//   - {now}            (yyyy-mm-dd hh:ii)
//   - {memories}       (rendered memory list)
//   - {history}        (JSON array of session messages)
package prompt

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Load reads a prompt file from path.
func Load(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", path, err)
	}
	return string(b), nil
}

// Render replaces template variables in tmpl with the provided values.
func Render(tmpl, userDescription, memories string, now time.Time) string {
	r := strings.NewReplacer(
		"{user_description}", userDescription,
		"{now}", now.Format("02 Jan 2006 15:04"),
		"{memories}", memories,
	)
	return r.Replace(tmpl)
}

// RenderWithHistory is like Render but also substitutes {history}.
func RenderWithHistory(tmpl, userDescription, memories, history string, now time.Time) string {
	r := strings.NewReplacer(
		"{user_description}", userDescription,
		"{now}", now.Format("02 Jan 2006 15:04"),
		"{memories}", memories,
		"{history}", history,
	)
	return r.Replace(tmpl)
}
