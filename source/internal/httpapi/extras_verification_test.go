package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type captureMailSender struct {
	mu      sync.Mutex
	to      string
	subject string
	body    string
}

func (s *captureMailSender) Send(_ context.Context, to, subject, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.to, s.subject, s.body = to, subject, body
	return nil
}

func TestRegistrationVerificationAndOfflineUUIDCompatibility(t *testing.T) {
	server, db := testServer(t)
	mail := &captureMailSender{}
	server.Config.MailSender = mail

	register := httptest.NewRequest(http.MethodPost, "/api?method=regist", strings.NewReader(url.Values{
		"name": {"Notch"}, "passwd": {"secret-password"}, "email": {"notch@example.test"},
	}.Encode()))
	register.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	registerResponse := httptest.NewRecorder()
	server.ServeHTTP(registerResponse, register)
	if registerResponse.Code != http.StatusOK || !strings.Contains(registerResponse.Body.String(), "验证码已发送") {
		t.Fatalf("注册请求失败: %d %s", registerResponse.Code, registerResponse.Body.String())
	}
	mail.mu.Lock()
	code := strings.TrimSpace(strings.TrimPrefix(mail.body, "验证码："))
	mail.mu.Unlock()
	if len(code) != 6 {
		t.Fatalf("邮件验证码格式错误: %q", code)
	}

	verify := httptest.NewRequest(http.MethodPost, "/api?method=verifyRegist", strings.NewReader(url.Values{
		"email": {"notch@example.test"}, "code": {code},
	}.Encode()))
	verify.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	verifyResponse := httptest.NewRecorder()
	server.ServeHTTP(verifyResponse, verify)
	if verifyResponse.Code != http.StatusOK || !strings.Contains(verifyResponse.Body.String(), `"success":true`) {
		t.Fatalf("注册验证码验证失败: %d %s", verifyResponse.Code, verifyResponse.Body.String())
	}
	var name, uuid string
	if err := db.QueryRow("SELECT name,uuid FROM users WHERE name_key='notch'").Scan(&name, &uuid); err != nil {
		t.Fatal(err)
	}
	if name != "Notch" || uuid != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("历史离线 UUID 不兼容: name=%s uuid=%s", name, uuid)
	}
}

func TestVerificationFailsClosedWithoutMailSender(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api?method=regist", strings.NewReader(url.Values{
		"name": {"NoMail"}, "passwd": {"secret-password"}, "email": {"nomail@example.test"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "发送验证邮件失败") {
		t.Fatalf("缺少邮件发送器时未失败关闭: %d %s", response.Code, response.Body.String())
	}
}

func TestWebhookUpdatesNameAndHostAtomically(t *testing.T) {
	server, db := testServer(t)
	server.Config.StatusServers = []StatusServerConfig{{Name: "线路A", Host: "mc.example.test", DefaultPort: 25565}}
	request := httptest.NewRequest(http.MethodPost, "/webhook/update-port", strings.NewReader(url.Values{
		"name": {"线路A"}, "host": {"mc.example.test"}, "port": {"25575"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("webhook 更新失败: %d %s", response.Code, response.Body.String())
	}
	var namePort, hostPort int
	if err := db.QueryRow("SELECT port FROM ports WHERE key='线路A'").Scan(&namePort); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT port FROM ports WHERE key='mc.example.test'").Scan(&hostPort); err != nil {
		t.Fatal(err)
	}
	if namePort != 25575 || hostPort != 25575 {
		t.Fatalf("webhook 双键端口不一致: name=%d host=%d", namePort, hostPort)
	}
}

func TestMinecraftStatusProtocolProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if _, err := readStatusPacket(conn); err != nil {
			serverDone <- err
			return
		}
		if _, err := readStatusPacket(conn); err != nil {
			serverDone <- err
			return
		}
		jsonBody := `{"version":{"name":"Paper 1.21"},"players":{"online":3,"max":20},"description":{"text":"测试服"}}`
		payload := append(encodeVarInt(0), encodeString(jsonBody)...)
		serverDone <- writeStatusPacket(conn, payload)
	}()

	server, _ := testServer(t)
	server.Config.StatusServers = []StatusServerConfig{{Name: "测试", Host: "127.0.0.1", DefaultPort: port}}
	server.Config.StatusTimeout = 2 * time.Second
	request := httptest.NewRequest(http.MethodGet, "/api/serverStatus", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态接口失败: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Servers []struct {
			Online  bool   `json:"online"`
			Version string `json:"version"`
			Motd    string `json:"motd"`
			Players struct {
				Online int `json:"online"`
				Max    int `json:"max"`
			} `json:"players"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || len(body.Servers) != 1 || !body.Servers[0].Online || body.Servers[0].Version != "Paper 1.21" || body.Servers[0].Motd != "测试服" || body.Servers[0].Players.Online != 3 || body.Servers[0].Players.Max != 20 {
		t.Fatalf("状态协议解析错误: %+v", body)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestMinecraftStatusFallsBackToConfiguredAPI(t *testing.T) {
	statusServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/127.0.0.1/25565" {
			http.Error(writer, "bad status API path", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"online":true,"players":{"online":4,"max":40},"version":{"name":"Paper fallback"},"description":{"text":"§aFallback"}}`))
	}))
	defer statusServer.Close()

	server, _ := testServer(t)
	server.Config.StatusServers = []StatusServerConfig{{Name: "fallback", Host: "127.0.0.1", DefaultPort: 25565}}
	server.Config.StatusAPIs = []StatusAPIConfig{{Name: "test-status", URLTemplate: statusServer.URL + "/{host}/{port}"}}
	server.Config.StatusTimeout = time.Second
	request := httptest.NewRequest(http.MethodGet, "/api/serverStatus", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("状态 fallback 请求失败: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Servers []struct {
			Online  bool   `json:"online"`
			Source  string `json:"source"`
			Version string `json:"version"`
			Motd    string `json:"motd"`
			Players struct {
				Online int `json:"online"`
				Max    int `json:"max"`
			} `json:"players"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || len(body.Servers) != 1 || !body.Servers[0].Online || body.Servers[0].Source != "test-status" || body.Servers[0].Version != "Paper fallback" || body.Servers[0].Motd != "Fallback" || body.Servers[0].Players.Online != 4 || body.Servers[0].Players.Max != 40 {
		t.Fatalf("状态 fallback 解析错误: %+v", body)
	}
}

func TestSMTPMailFormatting(t *testing.T) {
	message := buildPlainMail("GBC", "from@example.test", "to@example.test", "邮箱验证", "验证码：123456")
	for _, expected := range []string{"From:", "To: to@example.test", "Subject: =?UTF-8?B?", "验证码：123456"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("邮件格式缺少 %q: %s", expected, message)
		}
	}
	if strings.Contains(message, "secret") {
		t.Fatal("邮件格式不应包含 SMTP 凭据")
	}
}

func TestStatusHelpersRoundTrip(t *testing.T) {
	for _, value := range []int{0, 1, 127, 128, 255, 760, 1 << 20} {
		decoded, err := readVarInt(strings.NewReader(string(encodeVarInt(value))))
		if err != nil || decoded != value {
			t.Fatalf("VarInt roundtrip value=%d decoded=%d err=%v", value, decoded, err)
		}
	}
	if got := minecraftDescription(json.RawMessage(`{"text":"a","extra":[{"text":"b"}]}`)); got != "ab" {
		t.Fatalf("description flatten=%q", got)
	}
}

func TestLegacyStatusAPIResponseShapes(t *testing.T) {
	mcsrvstat, err := parseStatusAPIResponse("api.mcsrvstat.us", map[string]any{
		"online":  false,
		"players": map[string]any{"online": float64(0), "max": float64(20)},
		"version": "Paper 1.21",
		"motd":    map[string]any{"clean": []any{"第一行", "第二行"}},
		"debug":   map[string]any{"error": map[string]any{"query": "offline"}},
	})
	if err != nil || mcsrvstat["online"] != false || mcsrvstat["motd"] != "第一行\n第二行" || mcsrvstat["error"] != "offline" {
		t.Fatalf("mcsrvstat 旧格式解析错误: %#v %v", mcsrvstat, err)
	}
	mcapi, err := parseStatusAPIResponse("mcapi.us", map[string]any{
		"online":  true,
		"players": map[string]any{"now": float64(4), "max": float64(40)},
		"server":  map[string]any{"name": "Paper"},
		"motd":    "测试服",
	})
	if err != nil || mcapi["version"] != "Paper" || mcapi["motd"] != "测试服" {
		t.Fatalf("mcapi.us 旧格式解析错误: %#v %v", mcapi, err)
	}
	minetools, err := parseStatusAPIResponse("api.minetools.eu", map[string]any{
		"players":     float64(3),
		"version":     map[string]any{"text": "Vanilla"},
		"description": map[string]any{"text": "§a在线"},
	})
	if err != nil || minetools["online"] != true || minetools["version"] != "Vanilla" || minetools["motd"] != "在线" {
		t.Fatalf("minetools 旧格式解析错误: %#v %v", minetools, err)
	}
	if _, err := parseStatusAPIResponse("api.minetools.eu", map[string]any{"error": "offline"}); err == nil {
		t.Fatal("minetools error 字段应视为失败")
	}
}
