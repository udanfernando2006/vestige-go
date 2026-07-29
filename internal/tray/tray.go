package tray

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// quitting guards WindowClosing against firing during an intentional
// app.Quit() (tray "Quit", Cmd+Q/Alt+F4, or an OS-signal-triggered quit
// routed through app.Quit() in main.go). Without this, app.Quit()'s own
// internal window teardown fires WindowClosing same as a user clicking
// [X] — and the hook's Hide()+Cancel() then cancels the close app.Quit()
// itself needed to complete, leaving the window half-torn-down (observed:
// "Window #1 not found" + a WebView2/Win32 window-class unregister
// error immediately after). Setting this before every intentional-quit
// path lets the hook step aside and allow the real close through.
var quitting bool

func Setup(app *application.App, mainWindow *application.WebviewWindow, icon []byte) *application.SystemTray {
	systray := app.SystemTray.New()
	systray.SetIcon(icon)
	systray.SetLabel("Vestige")

	menu := app.NewMenu()
	menu.Add("Show Vestige").OnClick(func(ctx *application.Context) {
		ShowAndFocus(mainWindow)
	})
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(ctx *application.Context) {
		quitting = true
		app.Quit()
	})
	systray.SetMenu(menu)

	systray.OnClick(func() {
		if mainWindow.IsVisible() {
			mainWindow.Hide()
		} else {
			ShowAndFocus(mainWindow)
		}
	})

	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		mainWindow.Hide()
		e.Cancel()
	})

	return systray
}

func ShowAndFocus(w *application.WebviewWindow) {
	w.Show()
	w.UnMinimise()
	w.Focus()
}
