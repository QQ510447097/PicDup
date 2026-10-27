package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"picdup/internal/core"
	"picdup/internal/server"
)

//go:embed all:frontend
var embeddedFrontend embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	serverMode := flag.Bool("server", false, "run the HTTP server without a desktop window")
	port := flag.Int("port", 0, "HTTP port for server mode (0 picks a random free port)")
	dataDir := flag.String("data", "", "override the application data directory (useful for tests)")
	debugPort := flag.Int("debug-port", 0, "enable WebView2 remote debugging on this local port (development only)")
	flag.Parse()

	if err := run(*serverMode, *port, *dataDir, *debugPort); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

func run(serverMode bool, port int, dataDir string, debugPort int) error {
	eng, err := core.NewEngine(dataDir)
	if err != nil {
		return err
	}
	paths := eng.Paths()
	if serverMode {
		setupLog(paths, true)
	} else {
		setupLog(paths, false)
	}

	sub, err := fs.Sub(embeddedFrontend, "frontend")
	if err != nil {
		return err
	}
	static := application.BundledAssetFileServer(sub)

	if serverMode {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		srv := server.New(eng, server.Options{Desktop: false, Static: static})
		httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
		fmt.Printf("PicDup server listening on http://%s\n", ln.Addr().String())
		log.Printf("server mode listening on http://%s", ln.Addr().String())
		go func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
			<-ch
			_ = httpSrv.Close()
		}()
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}

	srv := server.New(eng, server.Options{Desktop: true, Static: static})

	windowsOpts := application.WindowsOptions{
		WebviewUserDataPath: paths.WebviewDataDir,
	}
	if debugPort > 0 {
		windowsOpts.AdditionalBrowserArgs = append(windowsOpts.AdditionalBrowserArgs,
			fmt.Sprintf("--remote-debugging-port=%d", debugPort))
	}
	app := application.New(application.Options{
		Name:        "PicDup",
		Description: "查找并安全清理重复与相似图片",
		Icon:        appIcon,
		Windows:     windowsOpts,
		Assets: application.AssetOptions{
			Handler:        srv.Handler(),
			DisableLogging: true,
		},
		OnShutdown: func() {
			eng.CancelScan()
			eng.Flush()
		},
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "PicDup · 图片去重与清理",
		Width:            1380,
		Height:           880,
		MinWidth:         1120,
		MinHeight:        700,
		BackgroundColour: application.NewRGB(11, 18, 32),
		URL:              "/",
		EnableFileDrop:   true,
		Windows: application.WindowsWindow{
			Theme: application.Dark,
		},
	})

	srv.SetPickFolder(func() (string, error) {
		return app.Dialog.OpenFile().
			CanChooseDirectories(true).
			CanChooseFiles(false).
			CanCreateDirectories(false).
			SetTitle("选择要扫描的文件夹").
			AttachToWindow(win).
			PromptForSingleSelection()
	})

	win.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		paths := event.Context().DroppedFiles()
		eng.AddDropped(paths)
	})

	if err := app.Run(); err != nil {
		return err
	}
	return nil
}

func setupLog(paths core.Paths, mirrorStderr bool) {
	f, err := os.OpenFile(filepath.Join(paths.LocalDir, "picdup.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	if mirrorStderr {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	} else {
		log.SetOutput(f)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
