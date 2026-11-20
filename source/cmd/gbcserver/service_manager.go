package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gbcserver/source/internal/store"
)

type serviceExit struct {
	name string
	code int
}

type managedRuntime struct {
	cancel  context.CancelFunc
	results chan serviceExit
	ready   chan string
}

type trayUpdate struct {
	webURL string
}

func runControlCommand(action string, args []string) int {
	commandName := action
	synopsis := "[--data-dir <data目录>]"
	flags := newCommandFlags(commandName, synopsis)
	dataDir := flags.String("data-dir", defaultDataDir(), "运行实例使用的 data 文件夹本身")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if action == "stop" && strings.TrimSpace(*dataDir) == "" {
		cliFprintln(os.Stderr, "--data-dir 不能为空")
		return 2
	}
	resolvedDataDir, err := filepath.Abs(strings.TrimSpace(*dataDir))
	if err != nil {
		cliFprintln(os.Stderr, "解析 --data-dir 失败:", err)
		return 2
	}
	if err := sendControlCommand(resolvedDataDir, action); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if action == "stop" {
		cliFprintln(os.Stdout, "停止请求已发送。")
	} else {
		cliFprintln(os.Stdout, "重载请求已发送。")
	}
	return 0
}

func runManagedStart(dataDir, _ string, tray bool) int {
	commands := make(chan managedCommand, 1)
	control, err := startControlServer(dataDir, commands)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer control.Close()

	processContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var trayDone <-chan error
	trayUpdates := make(chan trayUpdate, 1)
	if tray {
		result := make(chan error, 1)
		go func() { result <- runWindowsTray(dataDir, "", webURLFromDataDir(dataDir), trayUpdates) }()
		trayDone = result
	}

	webArgs, pluginArgs := managedServiceArgs(dataDir)
	adminControl := func(action string) error {
		if action == "install-autostart" {
			return configureAutostartPlatform(dataDir, true)
		}
		if action == "uninstall-autostart" {
			return configureAutostartPlatform(dataDir, false)
		}
		select {
		case commands <- managedCommand{Action: action}:
			return nil
		case <-processContext.Done():
			return processContext.Err()
		}
	}
	for {
		runtime := startManagedRuntime(processContext, webArgs, pluginArgs, adminControl)
		exited, err := waitManagedRuntimeReady(runtime, processContext)
		if err != nil {
			runtime.cancel()
			waitForServiceExits(runtime.results, 2-exited)
			if processContext.Err() != nil {
				return 0
			}
			cliFprintln(os.Stderr, "GBC Server 启动失败:", err)
			return 1
		}

	commandLoop:
		for {
			select {
			case command := <-commands:
				if command.Action == "reload" {
					runtime.cancel()
					waitForServiceExits(runtime.results, 2)
					candidateWebArgs, candidatePluginArgs := managedServiceArgs(dataDir)
					candidateRuntime := startManagedRuntime(processContext, candidateWebArgs, candidatePluginArgs, adminControl)
					candidateExited, startupErr := waitManagedRuntimeReady(candidateRuntime, processContext)
					if startupErr != nil {
						candidateRuntime.cancel()
						waitForServiceExits(candidateRuntime.results, 2-candidateExited)
						if processContext.Err() != nil {
							return 0
						}
						cliFprintln(os.Stderr, "数据库配置启动失败，正在恢复当前服务:", startupErr)
						webArgs, pluginArgs = managedServiceArgs(dataDir)
						restoredRuntime := startManagedRuntime(processContext, webArgs, pluginArgs, adminControl)
						restoredExited, restoreErr := waitManagedRuntimeReady(restoredRuntime, processContext)
						if restoreErr != nil {
							restoredRuntime.cancel()
							waitForServiceExits(restoredRuntime.results, 2-restoredExited)
							if processContext.Err() != nil {
								return 0
							}
							cliFprintln(os.Stderr, "恢复原配置也失败:", restoreErr)
							return 1
						}
						cliFprintln(os.Stderr, "当前数据库配置已恢复，服务继续运行。")
						runtime = restoredRuntime
						continue commandLoop
					}
					webArgs, pluginArgs = candidateWebArgs, candidatePluginArgs
					select {
					case trayUpdates <- trayUpdate{webURL: webURLFromDataDir(dataDir)}:
					default:
					}
					runtime = candidateRuntime
					cliFprintln(os.Stdout, "GBC Server 配置已重新加载；Web 与插件 listener 已就绪。")
					continue commandLoop
				}
				runtime.cancel()
				waitForServiceExits(runtime.results, 2)
				if command.Action == "stop" {
					cliFprintln(os.Stdout, "GBC Server 正在停止。")
					return 0
				}
			case result := <-runtime.results:
				runtime.cancel()
				waitForServiceExits(runtime.results, 1)
				if processContext.Err() != nil {
					return 0
				}
				if result.code == 0 {
					cliFprintf(os.Stderr, "%s 服务意外退出。\n", result.name)
					return 1
				}
				return result.code
			case <-processContext.Done():
				runtime.cancel()
				waitForServiceExits(runtime.results, 2)
				return 0
			case err := <-trayDone:
				runtime.cancel()
				waitForServiceExits(runtime.results, 2)
				if err != nil {
					cliFprintln(os.Stderr, "Windows 托盘退出:", err)
					return 1
				}
				return 0
			}
		}
	}
}

func startManagedRuntime(processContext context.Context, webArgs, pluginArgs []string, adminControl func(string) error) *managedRuntime {
	serviceContext, cancel := context.WithCancel(processContext)
	runtime := &managedRuntime{cancel: cancel, results: make(chan serviceExit, 2), ready: make(chan string, 2)}
	go func() {
		runtime.results <- serviceExit{name: "Web", code: runWebServeWithAdminControl(webArgs, serviceContext, runtime.ready, adminControl)}
	}()
	go func() {
		runtime.results <- serviceExit{name: "插件", code: runPluginServeWithReady(pluginArgs, serviceContext, runtime.ready)}
	}()
	return runtime
}

func waitManagedRuntimeReady(runtime *managedRuntime, processContext context.Context) (int, error) {
	ready := make(map[string]bool, 2)
	for len(ready) < 2 {
		select {
		case name := <-runtime.ready:
			ready[name] = true
		case result := <-runtime.results:
			return 1, fmt.Errorf("%s 服务在 listener 就绪前退出（退出码 %d）", result.name, result.code)
		case <-processContext.Done():
			return 0, processContext.Err()
		}
	}
	return 0, nil
}

func webURLFromDataDir(dataDir string) string {
	port := "80"
	db, err := openNormalizedDB(filepath.Join(dataDir, "gbcserver.db"))
	if err == nil {
		if value, ok, readErr := (store.ConfigStore{DB: db}).Get(context.Background(), "httpPort"); readErr == nil && ok && strings.TrimSpace(value) != "" {
			port = strings.TrimSpace(value)
		}
		_ = db.Close()
	}
	if _, err := strconv.Atoi(port); err != nil {
		port = "80"
	}
	return "http://127.0.0.1:" + port + "/"
}

func managedServiceArgs(dataDir string) ([]string, []string) {
	webArgs := []string{"--data-dir", dataDir}
	return webArgs, append([]string(nil), webArgs...)
}

func waitForServiceExits(results <-chan serviceExit, count int) {
	for index := 0; index < count; index++ {
		<-results
	}
}
