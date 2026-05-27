// Package alerter fires macOS desktop notifications for session-lens events.
// On non-darwin platforms all calls are silent no-ops.
package alerter

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Notifier is the interface used to send a desktop notification. The default
// implementation shells out to osascript; tests can inject a stub.
type Notifier interface {
	Notify(title, message string)
}

// Default returns the platform-appropriate Notifier. On darwin it returns an
// osascriptNotifier (unless SESSIONLENS_DESKTOP_ALERTS=0); on all other
// platforms it returns a noopNotifier.
func Default() Notifier {
	if runtime.GOOS != "darwin" {
		return noopNotifier{}
	}
	if os.Getenv("SESSIONLENS_DESKTOP_ALERTS") == "0" {
		return noopNotifier{}
	}
	return osascriptNotifier{}
}

// Notify is a package-level convenience that calls Default().Notify. Fire and
// forget: it runs inside a goroutine and does not block the caller.
func Notify(title, message string) {
	go Default().Notify(title, message)
}

// osascriptNotifier fires a macOS Notification Center notification.
type osascriptNotifier struct{}

func (osascriptNotifier) Notify(title, message string) {
	script := fmt.Sprintf(
		`display notification %q with title %q`,
		message, title,
	)
	cmd := exec.Command("osascript", "-e", script)
	// Ignore errors; notifications are best-effort.
	_ = cmd.Run()
}

// noopNotifier silently discards all notifications.
type noopNotifier struct{}

func (noopNotifier) Notify(_, _ string) {}
