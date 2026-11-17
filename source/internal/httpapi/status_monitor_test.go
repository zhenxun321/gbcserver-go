package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recordedStatusMail struct {
	mu       sync.Mutex
	Subjects []string
	Bodies   []string
}

func (m *recordedStatusMail) Send(_ context.Context, _, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Subjects = append(m.Subjects, subject)
	m.Bodies = append(m.Bodies, body)
	return nil
}

func TestStatusHistoryAvailabilityAndRecentLimit(t *testing.T) {
	server, db := testServer(t)
	now := time.Now().Truncate(time.Second)
	for index := 0; index < 61; index++ {
		online := index % 2
		if _, err := db.Exec(`
			INSERT INTO status_samples(server_key,server_name,host,port,online,status_json,checked_at)
			VALUES(?,?,?,?,?,'{}',?)`, "example.test:25565", "测试线路", "example.test", 25565,
			online, now.Add(-time.Duration(index)*time.Minute).UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}

	history, err := server.statusHistory(context.Background(), "example.test:25565", now)
	if err != nil {
		t.Fatal(err)
	}
	if history.SampleCount7d != 61 || len(history.Samples) != statusHistoryLimit || history.AvailabilityPercent < 49 || history.AvailabilityPercent > 50 {
		t.Fatalf("历史统计错误: count=%d availability=%.2f samples=%d", history.SampleCount7d, history.AvailabilityPercent, len(history.Samples))
	}
	if history.Samples[0].CheckedAt != now.Add(-59*time.Minute).UnixMilli() {
		t.Fatalf("历史样本应按时间正序返回，首项时间=%d", history.Samples[0].CheckedAt)
	}
}

func TestServerStatusReturnsPersistedSamples(t *testing.T) {
	server, db := testServer(t)
	server.Config.StatusServers = []StatusServerConfig{{Name: "主线路", Host: "example.test", DefaultPort: 25565}}
	server.Config.StatusAPIs = []StatusAPIConfig{}
	server.statusMonitorStarted = true
	checkedAt := time.Now().Add(-time.Minute).UnixMilli()
	if _, err := db.Exec(`
		INSERT INTO status_samples(server_key,server_name,host,port,online,status_json,checked_at)
		VALUES('example.test:25565','主线路','example.test',25565,1,
		'{"online":true,"source":"test","latencyMs":12}',?)`, checkedAt); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/serverStatus", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result struct {
		Success bool `json:"success"`
		Servers []struct {
			Online              bool                 `json:"online"`
			AvailabilityPercent float64              `json:"availabilityPercent"`
			SampleCount7d       int                  `json:"sampleCount7d"`
			CheckedAt           int64                `json:"checkedAt"`
			Samples             []statusHistoryPoint `json:"samples"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !result.Success || len(result.Servers) != 1 {
		t.Fatalf("状态接口读取失败: %d %s", response.Code, response.Body.String())
	}
	line := result.Servers[0]
	if !line.Online || line.AvailabilityPercent != 100 || line.SampleCount7d != 1 || line.CheckedAt != checkedAt || len(line.Samples) != 1 || !line.Samples[0].Online {
		t.Fatalf("状态接口未返回采样历史: %+v", line)
	}
}

func TestStatusAlertThresholdRecoveryAndBothChannels(t *testing.T) {
	server, _ := testServer(t)
	mail := &recordedStatusMail{}
	server.Config.StatusAlertAfter = 2 * time.Minute
	server.Config.StatusAlertEmailEnabled = true
	server.Config.StatusAlertEmail = "ops@example.test"
	server.Config.StatusAlertWebhookEnabled = true
	server.Config.StatusAlertWebhookURL = ""
	server.Config.StatusAlertRecoveryEnabled = true
	server.Config.MailSender = mail

	var webhookMu sync.Mutex
	webhookEvents := make([]string, 0, 2)
	webhook := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			Event   string `json:"event"`
			Status  string `json:"status"`
			Targets []struct {
				Name string `json:"name"`
			} `json:"targets"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("Webhook JSON 无效: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(payload.Targets) != 1 || payload.Targets[0].Name != "主线路" {
			t.Errorf("Webhook 缺少线路信息: %+v", payload)
		}
		webhookMu.Lock()
		webhookEvents = append(webhookEvents, payload.Event+":"+payload.Status)
		webhookMu.Unlock()
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer webhook.Close()
	server.Config.StatusAlertWebhookURL = webhook.URL
	targets := []statusTarget{{Name: "主线路", Host: "127.0.0.1", Port: 25565}}
	start := time.Now().Truncate(time.Second)

	for index, expected := range []string{"", "", "outage", "", "recovery"} {
		observedAt := start.Add(time.Duration(index) * time.Minute)
		allOffline := index < 4
		event, outageStartedAt, err := server.evaluateStatusAlert(context.Background(), allOffline, observedAt)
		if err != nil {
			t.Fatal(err)
		}
		if event != expected {
			t.Fatalf("第 %d 次检测事件=%q，期望 %q", index, event, expected)
		}
		if event != "" {
			if !outageStartedAt.Equal(start) {
				t.Fatalf("告警开始时间错误: %s", outageStartedAt)
			}
			server.dispatchStatusAlert(context.Background(), event, outageStartedAt, targets)
		}
	}
	if len(mail.Subjects) != 2 || mail.Subjects[0] == mail.Subjects[1] {
		t.Fatalf("邮件应各发送一次离线与恢复通知: %#v", mail.Subjects)
	}
	webhookMu.Lock()
	defer webhookMu.Unlock()
	if len(webhookEvents) != 2 || webhookEvents[0] != "outage:offline" || webhookEvents[1] != "recovery:recovered" {
		t.Fatalf("Webhook 告警与恢复事件错误: %#v", webhookEvents)
	}
}
