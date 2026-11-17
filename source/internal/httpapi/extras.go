package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gbcserver/source/internal/imageutil"
)

func (s *Server) handleUploadAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload struct {
		Username string `json:"username"`
		Avatar   string `json:"avatar"`
		Session  string `json:"session"`
	}
	if err := decodeBody(r, s.Config.BodyLimitBytes, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "参数错误"})
		return
	}
	if payload.Username == "" || payload.Avatar == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "参数错误"})
		return
	}
	username, ok := s.sessions.Get(firstNonempty(r.Header.Get("X-Web-Session"), payload.Session))
	if !ok || !strings.EqualFold(username, payload.Username) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	if !strings.HasPrefix(strings.ToLower(payload.Avatar), "data:image/png;base64,") || len(payload.Avatar) == len("data:image/png;base64,") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "仅支持PNG格式头像（data:image/png;base64,...)"})
		return
	}
	data, err := decodeDataURL(payload.Avatar, "image/png", s.Config.MaxAvatarBytes)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	if len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "头像不是有效的PNG文件"})
		return
	}
	compressed, _, err := imageutil.Compress(data, ".png", imageutil.Options{MaxDimension: 256, JPEGQuality: 82})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "头像图片无法解码"})
		return
	}
	data = compressed
	if s.avatarDir == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "头像目录未配置"})
		return
	}
	name, err := randomFilename(".png")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "上传失败"})
		return
	}
	path := filepath.Join(s.avatarDir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "上传失败"})
		return
	}
	if _, err := s.DB.ExecContext(r.Context(), "UPDATE users SET avatar_filename=?,updated_at=? WHERE name_key=?", name, time.Now().UnixMilli(), strings.ToLower(username)); err != nil {
		_ = os.Remove(path)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "上传失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "avatarUrl": "/avatars/" + name})
}

func (s *Server) handleUploadItemImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var data []byte
	var session string
	if contentType == "multipart/form-data" {
		// multipart 是新的首选上传方式；限制整包大小，避免先把超大请求读入内存。
		r.Body = http.MaxBytesReader(w, r.Body, s.Config.BodyLimitBytes)
		if err := r.ParseMultipartForm(s.Config.BodyLimitBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "商品图片数据不合法或过大"})
			return
		}
		session = firstNonempty(r.Header.Get("X-Admin-Session"), r.FormValue("session"))
		file, _, err := r.FormFile("file")
		if err != nil {
			file, _, err = r.FormFile("image")
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请选择商品图片文件"})
			return
		}
		defer file.Close()
		data, err = readUploadedImage(file, s.Config.MaxItemImageBytes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
	} else {
		// 保留旧版 JSON data URL 兼容性，管理页自身使用 multipart 文件上传。
		params, err := ParseLegacyParams(r, s.Config.BodyLimitBytes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "商品图片数据不合法"})
			return
		}
		session = firstNonempty(r.Header.Get("X-Admin-Session"), paramString(params["session"]))
		var payload struct {
			Image string `json:"image"`
			Name  string `json:"name"`
		}
		body, _ := json.Marshal(params)
		_ = json.Unmarshal(body, &payload)
		prefix, encoded, found := strings.Cut(strings.ToLower(payload.Image), ";base64,")
		if !found || encoded == "" || (prefix != "data:image/png" && prefix != "data:image/jpg" && prefix != "data:image/jpeg") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "仅支持 PNG/JPG/JPEG 图片"})
			return
		}
		data, err = decodeDataURL(payload.Image, "", s.Config.MaxItemImageBytes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
	}
	if _, ok := s.adminSessions.Get(session); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效或已过期"})
		return
	}
	ext := uploadedImageExtension(data)
	if ext == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "仅支持 PNG/JPG/JPEG 图片"})
		return
	}
	compressed, _, err := imageutil.Compress(data, ext, imageutil.Options{MaxDimension: 512, JPEGQuality: 82})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "商品图片无法解码"})
		return
	}
	data = compressed
	if s.staticDir == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "静态目录未配置"})
		return
	}
	dir := filepath.Join(s.staticDir, "items")
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "商品图片上传失败"})
		return
	}
	name, err := randomFilename(ext)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "商品图片上传失败"})
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "商品图片上传失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "imageUrl": "/items/" + name})
}

func readUploadedImage(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 5 * 1024 * 1024
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, errors.New("读取商品图片失败")
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("商品图片不能超过%d字节", limit)
	}
	if len(data) == 0 {
		return nil, errors.New("商品图片为空")
	}
	return data, nil
}

// 与旧上传接口一致，按文件签名识别格式，保存原始内容和正确扩展名。
func uploadedImageExtension(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return ".jpg"
	default:
		return ""
	}
}

func (s *Server) handleWebhookHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
}
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	params, err := ParseLegacyParams(r, s.Config.BodyLimitBytes)
	if err != nil {
		writePlain(w, 400, "Invalid JSON")
		return
	}
	name, host := paramString(params["name"]), paramString(params["host"])
	port, err := strconv.Atoi(paramString(params["port"]))
	if name == "" || host == "" || err != nil || port < 1 || port > 65535 {
		writePlain(w, 400, "Invalid parameters")
		return
	}
	matched := false
	for _, server := range s.Config.StatusServers {
		if server.Name == name {
			matched = true
			if server.Host != host {
				writePlain(w, 403, "Host mismatch")
				return
			}
			break
		}
	}
	if !matched {
		writePlain(w, 403, "Unknown server name")
		return
	}
	now := time.Now().UnixMilli()
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "INSERT INTO ports(key,port,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET port=excluded.port,updated_at=excluded.updated_at", name, port, now)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), "INSERT INTO ports(key,port,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET port=excluded.port,updated_at=excluded.updated_at", host, port, now)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	if err != nil {
		writePlain(w, 500, "Internal Server Error")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleServerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	targets, err := s.statusTargets(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取状态目标失败"})
		return
	}
	result := make([]map[string]any, 0, len(targets))
	now := time.Now()
	latestCheckedAt := now.UnixMilli()
	for _, target := range targets {
		var data map[string]any
		checkedAt := int64(0)
		if s.statusMonitorRunning() {
			data, checkedAt, err = s.latestStatusSample(r.Context(), target.Key)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取状态历史失败"})
				return
			}
		}
		if data == nil {
			data = s.cachedStatus(target.Key)
		}
		if data == nil {
			probeStarted := time.Now()
			data = s.probeStatus(r.Context(), target.Host, target.Port)
			if _, ok := data["latencyMs"]; !ok {
				data["latencyMs"] = time.Since(probeStarted).Milliseconds()
			}
			s.cacheStatus(target.Key, data)
			checkedAt = time.Now().UnixMilli()
		}
		history, err := s.statusHistory(r.Context(), target.Key, now)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取状态历史失败"})
			return
		}
		if checkedAt == 0 {
			checkedAt = now.UnixMilli()
		}
		if checkedAt > latestCheckedAt || len(result) == 0 {
			latestCheckedAt = checkedAt
		}
		item := map[string]any{
			"name": target.Name, "host": target.Host, "port": target.Port,
			"checkedAt": checkedAt, "availabilityPercent": history.AvailabilityPercent,
			"sampleCount7d": history.SampleCount7d, "samples": history.Samples,
		}
		for k, v := range data {
			item[k] = v
		}
		result = append(result, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "servers": result,
		"samplingIntervalMs": s.Config.StatusMonitorInterval.Milliseconds(),
		"nextSampleAt":       latestCheckedAt + s.Config.StatusMonitorInterval.Milliseconds(),
	})
}
func (s *Server) cachedStatus(key string) map[string]any {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	entry, ok := s.statusCache[key]
	if ok && time.Now().Before(entry.ExpiresAt) {
		return entry.Data
	}
	return nil
}
func (s *Server) cacheStatus(key string, data map[string]any) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.statusCache[key] = statusCacheEntry{Data: data, ExpiresAt: time.Now().Add(s.Config.StatusCacheTTL)}
}

// probeStatus 先尝试 Minecraft TCP，再按配置顺序尝试第三方状态 API。
// 直连成功时保持旧的低延迟路径；直连失败时第三方结果可恢复状态页可用性。
func (s *Server) probeStatus(ctx context.Context, host string, port int) map[string]any {
	started := time.Now()
	data := s.probeStatusResult(ctx, host, port)
	if data != nil {
		if _, exists := data["latencyMs"]; !exists {
			data["latencyMs"] = time.Since(started).Milliseconds()
		}
	}
	return data
}

func (s *Server) probeStatusResult(ctx context.Context, host string, port int) map[string]any {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	directResults := make(chan map[string]any, 1)
	fallbackResults := make(chan map[string]any, 1)
	go func() { directResults <- s.probeDirectStatus(probeCtx, host, port) }()
	go func() { fallbackResults <- s.probeStatusFallback(probeCtx, host, port) }()

	var direct map[string]any
	for completed := 0; completed < 2; completed++ {
		select {
		case result := <-directResults:
			direct = result
			if online, ok := result["online"].(bool); ok && online {
				return result
			}
		case result := <-fallbackResults:
			if result != nil {
				return result
			}
		case <-ctx.Done():
			if direct != nil {
				return direct
			}
			return statusProbeFailure(ctx.Err())
		}
	}
	if direct != nil {
		direct["source"] = "none"
		return direct
	}
	return statusProbeFailure(errors.New("状态检测失败"))
}

func (s *Server) probeDirectStatus(ctx context.Context, host string, port int) map[string]any {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: s.Config.StatusTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return map[string]any{"online": false, "players": map[string]int{"online": 0, "max": 0}, "version": "未知", "motd": "", "source": "direct", "error": err.Error()}
	}
	defer conn.Close()
	deadline := time.Now().Add(s.Config.StatusTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := writeStatusPacket(conn, buildStatusHandshake(host, port)); err != nil {
		return statusProbeFailure(err)
	}
	if err := writeStatusPacket(conn, []byte{0}); err != nil {
		return statusProbeFailure(err)
	}
	frame, err := readStatusPacket(conn)
	if err != nil {
		return statusProbeFailure(err)
	}
	reader := bytes.NewReader(frame)
	packetID, err := readVarInt(reader)
	if err != nil || packetID != 0 {
		return statusProbeFailure(errors.New("Minecraft status 响应类型无效"))
	}
	jsonLength, err := readVarInt(reader)
	if err != nil || jsonLength < 0 || jsonLength > 4*1024*1024 || int64(reader.Len()) < int64(jsonLength) {
		return statusProbeFailure(errors.New("Minecraft status JSON 长度无效"))
	}
	jsonData := make([]byte, jsonLength)
	if _, err := io.ReadFull(reader, jsonData); err != nil {
		return statusProbeFailure(err)
	}
	var payload struct {
		Version struct {
			Name string `json:"name"`
		} `json:"version"`
		Players struct {
			Online int `json:"online"`
			Max    int `json:"max"`
		} `json:"players"`
		Description json.RawMessage `json:"description"`
	}
	if err := json.Unmarshal(jsonData, &payload); err != nil {
		return statusProbeFailure(err)
	}
	return map[string]any{
		"online":  true,
		"players": map[string]int{"online": payload.Players.Online, "max": payload.Players.Max},
		"version": payload.Version.Name,
		"motd":    cleanStatusText(minecraftDescription(payload.Description)),
		"source":  "direct",
	}
}

func (s *Server) probeStatusFallback(ctx context.Context, host string, port int) map[string]any {
	apis := s.Config.StatusAPIs
	if len(apis) == 0 {
		return nil
	}
	timeout := s.Config.StatusTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	fallbackCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := make(chan map[string]any, len(apis))
	for _, api := range apis {
		api := api
		go func() {
			data, err := s.fetchStatusAPI(fallbackCtx, api, host, port)
			if err == nil {
				select {
				case results <- data:
				case <-fallbackCtx.Done():
				}
			}
		}()
	}
	for range apis {
		select {
		case result := <-results:
			return result
		case <-fallbackCtx.Done():
			return nil
		}
	}
	return nil
}

func (s *Server) fetchStatusAPI(ctx context.Context, api StatusAPIConfig, host string, port int) (map[string]any, error) {
	name := strings.TrimSpace(api.Name)
	if name == "" {
		name = "status-api"
	}
	target, err := expandStatusURL(api.URLTemplate, host, port)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s 返回 HTTP %d", name, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2*1024*1024 {
		return nil, errors.New("状态 API 响应过大")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	data, err := parseStatusAPIResponse(name, payload)
	if err != nil {
		return nil, err
	}
	data["source"] = name
	data["latencyMs"] = time.Since(started).Milliseconds()
	return data, nil
}

func expandStatusURL(template, host string, port int) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", errors.New("状态 API URL 为空")
	}
	if !strings.HasPrefix(strings.ToLower(template), "https://") && !strings.HasPrefix(strings.ToLower(template), "http://") {
		return "", errors.New("状态 API URL 必须使用 HTTP 或 HTTPS")
	}
	hostValue := strings.TrimSpace(host)
	if hostValue == "" || port < 1 || port > 65535 {
		return "", errors.New("状态目标地址无效")
	}
	hostPath := url.PathEscape(hostValue)
	hostQuery := url.QueryEscape(hostValue)
	hostPort := hostPath + ":" + strconv.Itoa(port)
	if strings.Contains(hostValue, ":") {
		hostPort = url.PathEscape(net.JoinHostPort(hostValue, strconv.Itoa(port)))
	}
	return strings.NewReplacer(
		"{hostport}", hostPort,
		"{host}", hostQuery,
		"{port}", strconv.Itoa(port),
	).Replace(template), nil
}

func parseStatusAPIResponse(name string, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, errors.New("状态 API 返回空对象")
	}
	if statusAPITruthy(payload["error"]) && strings.Contains(strings.ToLower(name), "minetools") {
		errorText := statusAPIErrorText(payload)
		if errorText == "" {
			errorText = "状态 API 返回错误"
		}
		return nil, errors.New(errorText)
	}
	online, hasOnline := payload["online"].(bool)
	if !hasOnline {
		// api.minetools.eu 没有 online 字段；旧 Node 版本以没有 error 视为在线。
		online = strings.Contains(strings.ToLower(name), "minetools") && payload["error"] == nil
	}
	players := map[string]int{"online": 0, "max": 0}
	if value, ok := payload["players"].(map[string]any); ok {
		players["online"] = statusNumber(value["online"])
		if players["online"] == 0 {
			players["online"] = statusNumber(value["now"])
		}
		players["max"] = statusNumber(value["max"])
	} else {
		players["online"] = statusNumber(payload["players"])
	}
	version := statusVersionText(payload["version"])
	if value, ok := payload["server"].(map[string]any); ok && version == "" {
		version = statusText(value["name"])
	}
	motd := statusText(payload["motd"])
	if motd == "" {
		motd = statusText(payload["description"])
	}
	data := map[string]any{
		"online":  online,
		"players": players,
		"version": cleanStatusText(version),
		"motd":    cleanStatusText(motd),
	}
	if !online {
		errorText := statusAPIErrorText(payload)
		if errorText != "" {
			data["error"] = errorText
		}
	}
	if data["version"] == "" {
		data["version"] = "未知"
	}
	return data, nil
}

func statusAPITruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return true
	}
}

func statusVersionText(value any) string {
	if object, ok := value.(map[string]any); ok {
		if text := statusText(object); text != "" {
			return text
		}
		encoded, err := json.Marshal(object)
		if err == nil {
			return string(encoded)
		}
	}
	return statusText(value)
}

func statusAPIErrorText(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if text := cleanStatusText(statusText(payload["error"])); text != "" {
		return text
	}
	if statusAPITruthy(payload["error"]) {
		encoded, err := json.Marshal(payload["error"])
		if err == nil {
			return cleanStatusText(string(encoded))
		}
	}
	debug, ok := payload["debug"].(map[string]any)
	if !ok {
		return ""
	}
	if text := cleanStatusText(statusText(debug["error"])); text != "" {
		return text
	}
	if values, ok := debug["error"].(map[string]any); ok {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if text := cleanStatusText(statusText(values[key])); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "; ")
	}
	return ""
}

func statusNumber(value any) int {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := strconv.Atoi(typed.String())
		return maxStatusNumber(parsed)
	case float64:
		return maxStatusNumber(int(typed))
	case int:
		return maxStatusNumber(typed)
	case int64:
		return maxStatusNumber(int(typed))
	default:
		parsed, _ := strconv.Atoi(strings.TrimSpace(statusText(value)))
		return maxStatusNumber(parsed)
	}
}

func maxStatusNumber(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func statusText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := statusText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text := statusText(typed["clean"]); text != "" {
			return text
		}
		if text := statusText(typed["name"]); text != "" {
			return text
		}
		if text := statusText(typed["text"]); text != "" {
			return text
		}
		if text := statusText(typed["extra"]); text != "" {
			return text
		}
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func cleanStatusText(value string) string {
	runes := []rune(value)
	cleaned := make([]rune, 0, len(runes))
	for index := 0; index < len(runes); index++ {
		if runes[index] == '§' && index+1 < len(runes) {
			index++
			continue
		}
		if runes[index] < 32 && runes[index] != '\n' && runes[index] != '\t' {
			continue
		}
		cleaned = append(cleaned, runes[index])
	}
	lines := strings.Split(string(cleaned), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			kept = append(kept, line)
		}
	}
	result := strings.Join(kept, "\n")
	if len([]rune(result)) > 300 {
		result = string([]rune(result)[:300])
	}
	return result
}

func statusProbeFailure(err error) map[string]any {
	return map[string]any{"online": false, "players": map[string]int{"online": 0, "max": 0}, "version": "未知", "motd": "", "source": "direct", "error": err.Error()}
}

func buildStatusHandshake(host string, port int) []byte {
	payload := make([]byte, 0, 64)
	payload = append(payload, encodeVarInt(0)...)
	payload = append(payload, encodeVarInt(760)...)
	payload = append(payload, encodeString(host)...)
	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	payload = append(payload, portBytes[:]...)
	payload = append(payload, encodeVarInt(1)...)
	return payload
}

func writeStatusPacket(writer io.Writer, payload []byte) error {
	frame := append(encodeVarInt(len(payload)), payload...)
	_, err := writer.Write(frame)
	return err
}

func readStatusPacket(reader io.Reader) ([]byte, error) {
	length, err := readVarInt(reader)
	if err != nil {
		return nil, err
	}
	if length < 0 || length > 4*1024*1024 {
		return nil, errors.New("Minecraft status 数据包过大")
	}
	frame := make([]byte, length)
	_, err = io.ReadFull(reader, frame)
	return frame, err
}

func encodeVarInt(value int) []byte {
	if value < 0 {
		value = int(uint32(value))
	}
	encoded := make([]byte, 0, 5)
	for {
		current := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			current |= 0x80
		}
		encoded = append(encoded, current)
		if value == 0 {
			return encoded
		}
	}
}

func readVarInt(reader io.Reader) (int, error) {
	value := 0
	for shift := 0; shift < 35; shift += 7 {
		var single [1]byte
		if _, err := io.ReadFull(reader, single[:]); err != nil {
			return 0, err
		}
		current := single[0]
		value |= int(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, nil
		}
	}
	return 0, errors.New("Minecraft VarInt 过长")
}

func encodeString(value string) []byte {
	data := []byte(value)
	return append(encodeVarInt(len(data)), data...)
}

func minecraftDescription(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var component struct {
		Text  string            `json:"text"`
		Extra []json.RawMessage `json:"extra"`
	}
	if json.Unmarshal(raw, &component) != nil {
		return ""
	}
	result := component.Text
	for _, extra := range component.Extra {
		result += minecraftDescription(extra)
	}
	return result
}

func decodeDataURL(value, requiredMime string, max int64) ([]byte, error) {
	parts := strings.SplitN(value, ",", 2)
	if len(parts) != 2 || !strings.HasPrefix(strings.ToLower(parts[0]), "data:") {
		return nil, errors.New("图片数据不合法")
	}
	meta := strings.TrimPrefix(strings.ToLower(parts[0]), "data:")
	semi := strings.Index(meta, ";")
	if semi < 0 || !strings.Contains(meta[semi:], "base64") {
		return nil, errors.New("图片数据不合法")
	}
	mimeType := meta[:semi]
	if requiredMime != "" && mimeType != requiredMime {
		return nil, errors.New("仅支持PNG格式头像（data:image/png;base64,...)")
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(decoded) == 0 || int64(len(decoded)) > max {
		return nil, errors.New("图片数据不合法或过大")
	}
	return decoded, nil
}
func randomFilename(ext string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]) + ext, nil
}
func firstNonempty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

var _ = fmt.Sprint
