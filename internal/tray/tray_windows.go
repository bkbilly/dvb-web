//go:build windows

package tray

import (
	"context"
	"fmt"
	"log"
	"os"

	"dvb-web/web"

	"github.com/gogpu/systray"
)

// Run starts the Windows system tray icon and event loop.
// It blocks until the user selects 'Exit' or the context is cancelled.
func Run(ctx context.Context, port int, onExit func()) error {
	uiURL := fmt.Sprintf("http://localhost:%d", port)
	tray := systray.New()

	iconData := web.GetIconPNG()
	if len(iconData) > 0 {
		tray.SetIcon(iconData)
	}
	tray.SetTooltip(fmt.Sprintf("DVB-Web (%s)", uiURL))

	menu := systray.NewMenu()
	menu.Add("Open Web UI", func() {
		_ = OpenBrowser(uiURL)
	})
	menu.AddSeparator()
	menu.Add("Exit", func() {
		log.Println("[Tray] Exit requested by user")
		tray.Remove()
		if onExit != nil {
			onExit()
		}
		os.Exit(0)
	})

	tray.SetMenu(menu)
	tray.OnClick(func() {
		_ = OpenBrowser(uiURL)
	})
	tray.Show()

	go func() {
		<-ctx.Done()
		tray.Remove()
		os.Exit(0)
	}()

	log.Printf("[Tray] System tray icon active in taskbar. URL: %s", uiURL)
	return tray.Run()
}
