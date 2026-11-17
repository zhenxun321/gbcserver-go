//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	trayWindowMessage = 0x8001
	wmDestroy         = 0x0002
	wmCommand         = 0x0111
	wmContextMenu     = 0x007B
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmNull            = 0x0000
	wmQuit            = 0x0012
	wmApp             = 0x8000
	wsExToolWindow    = 0x00000080
	wsPopup           = 0x80000000
	idiApplication    = 32512
	idcArrow          = 32512
	showNormal        = 1
	showHide          = 0
	nimAdd            = 0
	nimDelete         = 2
	nifMessage        = 1
	nifIcon           = 2
	nifTip            = 4
	mfString          = 0x00000000
	tpmReturnCmd      = 0x00000100
	tpmRightButton    = 0x00000002
	menuData          = 1001
	menuWeb           = 1002
	menuLog           = 1003
	menuExit          = 1004
)

type trayPoint struct {
	X int32
	Y int32
}

type trayWndClassEx struct {
	CbSize     uint32
	Style      uint32
	WndProc    uintptr
	CbClsExtra int32
	CbWndExtra int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	SmallIcon  windows.Handle
}

type trayMsg struct {
	Wnd     windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   trayPoint
	Private uint32
}

type notifyIconDataW struct {
	CbSize           uint32
	Wnd              windows.Handle
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             windows.Handle
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	Guid             [16]byte
	BalloonIcon      windows.Handle
}

type windowsTray struct {
	hwnd     windows.Handle
	dataDir  string
	logPath  string
	webURLMu sync.RWMutex
	webURL   string
}

var activeWindowsTray *windowsTray

var (
	trayUser32        = syscall.NewLazyDLL("user32.dll")
	trayKernel32      = syscall.NewLazyDLL("kernel32.dll")
	trayShell32       = syscall.NewLazyDLL("shell32.dll")
	procRegister      = trayUser32.NewProc("RegisterClassExW")
	procUnregister    = trayUser32.NewProc("UnregisterClassW")
	procCreateWindow  = trayUser32.NewProc("CreateWindowExW")
	procDefWindow     = trayUser32.NewProc("DefWindowProcW")
	procDestroy       = trayUser32.NewProc("DestroyWindow")
	procGetMessage    = trayUser32.NewProc("GetMessageW")
	procTranslate     = trayUser32.NewProc("TranslateMessage")
	procDispatch      = trayUser32.NewProc("DispatchMessageW")
	procPostQuit      = trayUser32.NewProc("PostQuitMessage")
	procPostMessage   = trayUser32.NewProc("PostMessageW")
	procGetCursor     = trayUser32.NewProc("GetCursorPos")
	procSetForeground = trayUser32.NewProc("SetForegroundWindow")
	procCreateMenu    = trayUser32.NewProc("CreatePopupMenu")
	procAppendMenu    = trayUser32.NewProc("AppendMenuW")
	procTrackMenu     = trayUser32.NewProc("TrackPopupMenu")
	procDestroyMenu   = trayUser32.NewProc("DestroyMenu")
	procLoadIcon      = trayUser32.NewProc("LoadIconW")
	procLoadCursor    = trayUser32.NewProc("LoadCursorW")
	procGetModule     = trayKernel32.NewProc("GetModuleHandleW")
	procShellNotify   = trayShell32.NewProc("Shell_NotifyIconW")
	procShellExecute  = trayShell32.NewProc("ShellExecuteW")
)

func runWindowsTray(dataDir, _ string, webURL string, updates <-chan trayUpdate) error {
	dataDir, err := filepath.Abs(strings.TrimSpace(dataDir))
	if err != nil {
		return fmt.Errorf("解析托盘数据目录失败: %w", err)
	}
	className, err := windows.UTF16PtrFromString("GBCServerTrayWindow")
	if err != nil {
		return err
	}
	module, _, _ := procGetModule.Call(0)
	icon, _, _ := procLoadIcon.Call(0, uintptr(idiApplication))
	cursor, _, _ := procLoadCursor.Call(0, uintptr(idcArrow))
	windowProc := windows.NewCallback(trayWindowProc)
	class := trayWndClassEx{CbSize: uint32(unsafe.Sizeof(trayWndClassEx{})), WndProc: windowProc, Instance: windows.Handle(module), Icon: windows.Handle(icon), Cursor: windows.Handle(cursor), ClassName: className, SmallIcon: windows.Handle(icon)}
	procRegister.Call(uintptr(unsafe.Pointer(&class)))
	hwnd, _, callErr := procCreateWindow.Call(wsExToolWindow, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), wsPopup, 0, 0, 0, 0, 0, 0, module, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建 Windows 托盘窗口失败: %w", callErr)
	}
	tray := &windowsTray{hwnd: windows.Handle(hwnd), dataDir: dataDir, logPath: filepath.Join(dataDir, "gbcserver.log"), webURL: webURL}
	activeWindowsTray = tray
	updatesDone := make(chan struct{})
	go func() {
		defer close(updatesDone)
		for update := range updates {
			tray.webURLMu.Lock()
			tray.webURL = update.webURL
			tray.webURLMu.Unlock()
		}
	}()
	defer func() {
		activeWindowsTray = nil
		<-updatesDone
		tray.removeIcon()
		procDestroy.Call(hwnd)
		procUnregister.Call(uintptr(unsafe.Pointer(className)), module)
	}()
	tray.addIcon(icon)
	var message trayMsg
	for {
		result, _, getErr := procGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int64(result) == -1 {
			return fmt.Errorf("读取托盘消息失败: %w", getErr)
		}
		if result == 0 {
			return nil
		}
		procTranslate.Call(uintptr(unsafe.Pointer(&message)))
		procDispatch.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func trayWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	tray := activeWindowsTray
	if tray != nil {
		switch message {
		case trayWindowMessage, wmContextMenu, wmRButtonUp:
			tray.showMenu()
			return 0
		case wmLButtonUp:
			tray.openTarget(tray.currentWebURL())
			return 0
		case wmCommand:
			tray.executeMenu(uint32(wParam & 0xffff))
			return 0
		case wmDestroy:
			procPostQuit.Call(0)
			return 0
		}
	}
	result, _, _ := procDefWindow.Call(hwnd, uintptr(message), wParam, lParam)
	return result
}

func (tray *windowsTray) addIcon(icon uintptr) {
	data := notifyIconDataW{CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})), Wnd: tray.hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: trayWindowMessage, Icon: windows.Handle(icon)}
	tip, _ := windows.UTF16FromString("GBC Server")
	copy(data.Tip[:], tip)
	procShellNotify.Call(nimAdd, uintptr(unsafe.Pointer(&data)))
}

func (tray *windowsTray) removeIcon() {
	data := notifyIconDataW{CbSize: uint32(unsafe.Sizeof(notifyIconDataW{})), Wnd: tray.hwnd, ID: 1}
	procShellNotify.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

func (tray *windowsTray) showMenu() {
	menu, _, _ := procCreateMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	tray.appendMenu(menu, menuData, "打开数据目录")
	tray.appendMenu(menu, menuLog, "打开运行日志")
	tray.appendMenu(menu, menuWeb, "打开 Web 页面")
	tray.appendMenu(menu, menuExit, "退出 GBC Server")
	var point trayPoint
	procGetCursor.Call(uintptr(unsafe.Pointer(&point)))
	procSetForeground.Call(uintptr(tray.hwnd))
	selected, _, _ := procTrackMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(point.X), uintptr(point.Y), 0, uintptr(tray.hwnd), 0)
	procPostMessage.Call(uintptr(tray.hwnd), wmNull, 0, 0)
	if selected != 0 {
		tray.executeMenu(uint32(selected))
	}
}

func (tray *windowsTray) appendMenu(menu uintptr, identifier uint32, label string) {
	text, _ := windows.UTF16PtrFromString(label)
	procAppendMenu.Call(menu, mfString, uintptr(identifier), uintptr(unsafe.Pointer(text)))
}

func (tray *windowsTray) executeMenu(identifier uint32) {
	switch identifier {
	case menuData:
		tray.openTarget(tray.dataDir)
	case menuLog:
		tray.openTarget(tray.logPath)
	case menuWeb:
		tray.openTarget(tray.currentWebURL())
	case menuExit:
		procPostMessage.Call(uintptr(tray.hwnd), wmDestroy, 0, 0)
	}
}

func (tray *windowsTray) currentWebURL() string {
	tray.webURLMu.RLock()
	defer tray.webURLMu.RUnlock()
	if strings.TrimSpace(tray.webURL) == "" {
		return "http://127.0.0.1/"
	}
	return tray.webURL
}

func (tray *windowsTray) openTarget(target string) {
	verb, _ := windows.UTF16PtrFromString("open")
	path, _ := windows.UTF16PtrFromString(target)
	procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(path)), 0, 0, showNormal)
}
