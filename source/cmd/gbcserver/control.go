package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const controlFileName = ".gbcserver-control.json"

type managedCommand struct {
	Action string
}

type controlState struct {
	Network string `json:"network"`
	Address string `json:"address"`
	Token   string `json:"token"`
}

type controlRequest struct {
	Token  string `json:"token"`
	Action string `json:"action"`
}

type controlResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type controlServer struct {
	listener net.Listener
	path     string
	state    controlState
	commands chan<- managedCommand
	closed   chan struct{}
}

func controlEndpoint(dataDir string) (string, string) {
	if runtime.GOOS == "linux" {
		return "unix", filepath.Join(dataDir, ".gbcserver-control.sock")
	}
	return "tcp", "127.0.0.1:0"
}

func startControlServer(dataDir string, commands chan<- managedCommand) (*controlServer, error) {
	network, address := controlEndpoint(dataDir)
	if network == "unix" {
		_ = os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("创建本机管理通道失败: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		listener.Close()
		return nil, fmt.Errorf("生成管理通道令牌失败: %w", err)
	}
	state := controlState{Network: network, Address: listener.Addr().String(), Token: hex.EncodeToString(tokenBytes)}
	if network == "unix" {
		state.Address = address
		_ = os.Chmod(address, 0o600)
	}
	path := filepath.Join(dataDir, controlFileName)
	if err := writeControlState(path, state); err != nil {
		listener.Close()
		return nil, err
	}
	server := &controlServer{listener: listener, path: path, state: state, commands: commands, closed: make(chan struct{})}
	go server.accept()
	return server, nil
}

func writeControlState(path string, state controlState) error {
	content, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("生成管理通道状态失败: %w", err)
	}
	_ = os.Remove(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("写入管理通道状态失败: %w", err)
	}
	if _, err := file.Write(append(content, '\n')); err != nil {
		file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("写入管理通道状态失败: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("关闭管理通道状态失败: %w", err)
	}
	return nil
}

func (server *controlServer) accept() {
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			select {
			case <-server.closed:
				return
			default:
			}
			continue
		}
		go server.handle(connection)
	}
}

func (server *controlServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(io.LimitReader(connection, 8192))
	var request controlRequest
	if err := decoder.Decode(&request); err != nil {
		writeControlResponse(connection, controlResponse{Message: "管理请求格式无效"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Token), []byte(server.state.Token)) != 1 {
		writeControlResponse(connection, controlResponse{Message: "管理通道认证失败"})
		return
	}
	if request.Action != "stop" && request.Action != "reload" {
		writeControlResponse(connection, controlResponse{Message: "不支持的管理命令"})
		return
	}
	select {
	case server.commands <- managedCommand{Action: request.Action}:
		writeControlResponse(connection, controlResponse{OK: true, Message: "管理请求已接收"})
	case <-server.closed:
		writeControlResponse(connection, controlResponse{Message: "服务正在退出"})
	}
}

func writeControlResponse(connection net.Conn, response controlResponse) {
	_ = json.NewEncoder(connection).Encode(response)
}

func (server *controlServer) Close() {
	if server == nil {
		return
	}
	select {
	case <-server.closed:
		return
	default:
		close(server.closed)
	}
	_ = server.listener.Close()
	if server.state.Network == "unix" {
		_ = os.Remove(server.state.Address)
	}
	data, err := os.ReadFile(server.path)
	if err != nil {
		return
	}
	var current controlState
	if json.Unmarshal(data, &current) == nil && current.Token == server.state.Token {
		_ = os.Remove(server.path)
	}
}

func sendControlCommand(dataDir, action string) error {
	path := filepath.Join(dataDir, controlFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("管理通道状态文件不存在；请先启动服务，并确认 --data-dir 与 start 使用的目录一致（%s）", path)
		}
		return fmt.Errorf("讀取管理通道失敗；服務可能尚未啟動: %w", err)
	}
	var state controlState
	if err := json.Unmarshal(content, &state); err != nil || strings.TrimSpace(state.Address) == "" || strings.TrimSpace(state.Token) == "" {
		return errors.New("管理通道狀態無效；請確認 data 目錄與運行實例一致")
	}
	network := strings.TrimSpace(state.Network)
	if network == "" {
		network = "tcp"
	}
	connection, err := net.DialTimeout(network, state.Address, 3*time.Second)
	if err != nil {
		return fmt.Errorf("無法連接管理通道；服務可能尚未啟動: %w", err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	request := controlRequest{Token: state.Token, Action: action}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("发送管理请求失败: %w", err)
	}
	var response controlResponse
	if err := json.NewDecoder(io.LimitReader(connection, 8192)).Decode(&response); err != nil {
		return fmt.Errorf("读取管理响应失败: %w", err)
	}
	if !response.OK {
		return errors.New(response.Message)
	}
	return nil
}
