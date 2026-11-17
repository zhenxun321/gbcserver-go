package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	statusHistoryWindow = 7 * 24 * time.Hour
	statusHistoryLimit  = 60
	statusProbeWorkers  = 4
)

type statusTarget struct {
	Name string
	Host string
	Port int
	Key  string
}

type statusHistoryPoint struct {
	Online    bool  `json:"online"`
	CheckedAt int64 `json:"checkedAt"`
}

type statusHistorySummary struct {
	AvailabilityPercent float64              `json:"availabilityPercent"`
	SampleCount7d       int                  `json:"sampleCount7d"`
	Samples             []statusHistoryPoint `json:"samples"`
}

// StartStatusMonitor 在服务启动时立即采样，之后按配置间隔轮询状态目标。
func (s *Server) StartStatusMonitor(ctx context.Context) {
	s.statusMu.Lock()
	if s.statusMonitorStarted {
		s.statusMu.Unlock()
		return
	}
	s.statusMonitorStarted = true
	s.statusMu.Unlock()
	go s.runStatusMonitor(ctx)
}

func (s *Server) statusMonitorRunning() bool {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.statusMonitorStarted
}

func (s *Server) runStatusMonitor(ctx context.Context) {
	ticker := time.NewTicker(s.Config.StatusMonitorInterval)
	defer ticker.Stop()
	for {
		if err := s.sampleStatusTargets(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("状态监控采样失败: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) sampleStatusTargets(ctx context.Context) error {
	targets, err := s.statusTargets(ctx)
	if err != nil || len(targets) == 0 {
		return err
	}
	results := make([]map[string]any, len(targets))
	workers := statusProbeWorkers
	if len(targets) < workers {
		workers = len(targets)
	}
	for start := 0; start < len(targets); start += workers {
		end := start + workers
		if end > len(targets) {
			end = len(targets)
		}
		batch := make(chan struct {
			index int
			data  map[string]any
		}, end-start)
		for index := start; index < end; index++ {
			index := index
			go func() {
				probeStarted := time.Now()
				probeCtx, cancel := context.WithTimeout(ctx, s.Config.StatusTimeout+time.Second)
				data := s.probeStatus(probeCtx, targets[index].Host, targets[index].Port)
				cancel()
				if _, ok := data["latencyMs"]; !ok {
					data["latencyMs"] = time.Since(probeStarted).Milliseconds()
				}
				batch <- struct {
					index int
					data  map[string]any
				}{index: index, data: data}
			}()
		}
		for range end - start {
			result := <-batch
			results[result.index] = result.data
		}
	}

	checkedAt := time.Now()
	allOffline := true
	for index, target := range targets {
		data := results[index]
		online, _ := data["online"].(bool)
		if online {
			allOffline = false
		}
		if err := s.persistStatusSample(ctx, target, data, checkedAt); err != nil {
			return err
		}
		s.cacheStatus(target.Key, data)
	}
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM status_samples WHERE checked_at < ?", checkedAt.Add(-statusHistoryWindow).UnixMilli()); err != nil {
		return err
	}
	event, outageStartedAt, err := s.evaluateStatusAlert(ctx, allOffline, checkedAt)
	if err != nil {
		return err
	}
	if event != "" {
		s.dispatchStatusAlert(ctx, event, outageStartedAt, targets)
	}
	return nil
}

func (s *Server) statusTargets(ctx context.Context) ([]statusTarget, error) {
	targets := make([]statusTarget, 0, len(s.Config.StatusServers))
	for _, configured := range s.Config.StatusServers {
		port := configured.DefaultPort
		if err := s.DB.QueryRowContext(ctx, "SELECT port FROM ports WHERE key=?", configured.Name).Scan(&port); err != nil {
			_ = s.DB.QueryRowContext(ctx, "SELECT port FROM ports WHERE key=?", configured.Host).Scan(&port)
		}
		key := net.JoinHostPort(configured.Host, strconv.Itoa(port))
		targets = append(targets, statusTarget{Name: configured.Name, Host: configured.Host, Port: port, Key: key})
	}
	return targets, nil
}

func (s *Server) persistStatusSample(ctx context.Context, target statusTarget, data map[string]any, checkedAt time.Time) error {
	online, _ := data["online"].(bool)
	latency, hasLatency := statusLatency(data["latencyMs"])
	stored := make(map[string]any, 6)
	for _, key := range []string{"online", "players", "version", "motd", "source", "error"} {
		if value, exists := data[key]; exists {
			stored[key] = value
		}
	}
	if hasLatency {
		stored["latencyMs"] = latency
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	var latencyValue any
	if hasLatency {
		latencyValue = latency
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO status_samples(server_key,server_name,host,port,online,latency_ms,status_json,checked_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		target.Key, target.Name, target.Host, target.Port, online, latencyValue, string(payload), checkedAt.UnixMilli())
	return err
}

func statusLatency(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	default:
		return 0, false
	}
}

func (s *Server) latestStatusSample(ctx context.Context, key string) (map[string]any, int64, error) {
	var payload string
	var checkedAt int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT status_json,checked_at FROM status_samples
		WHERE server_key=? ORDER BY checked_at DESC,id DESC LIMIT 1`, key).Scan(&payload, &checkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	data := make(map[string]any)
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		return nil, 0, err
	}
	return data, checkedAt, nil
}

func (s *Server) statusHistory(ctx context.Context, key string, now time.Time) (statusHistorySummary, error) {
	var total, online int
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(SUM(online),0) FROM status_samples
		WHERE server_key=? AND checked_at>=?`, key, now.Add(-statusHistoryWindow).UnixMilli()).Scan(&total, &online); err != nil {
		return statusHistorySummary{}, err
	}
	summary := statusHistorySummary{SampleCount7d: total, Samples: make([]statusHistoryPoint, 0, statusHistoryLimit)}
	if total > 0 {
		summary.AvailabilityPercent = float64(online) * 100 / float64(total)
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT online,checked_at FROM status_samples
		WHERE server_key=? ORDER BY checked_at DESC,id DESC LIMIT ?`, key, statusHistoryLimit)
	if err != nil {
		return statusHistorySummary{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var point statusHistoryPoint
		var onlineValue int
		if err := rows.Scan(&onlineValue, &point.CheckedAt); err != nil {
			return statusHistorySummary{}, err
		}
		point.Online = onlineValue != 0
		summary.Samples = append(summary.Samples, point)
	}
	if err := rows.Err(); err != nil {
		return statusHistorySummary{}, err
	}
	for left, right := 0, len(summary.Samples)-1; left < right; left, right = left+1, right-1 {
		summary.Samples[left], summary.Samples[right] = summary.Samples[right], summary.Samples[left]
	}
	return summary, nil
}

func (s *Server) evaluateStatusAlert(ctx context.Context, allOffline bool, observedAt time.Time) (string, time.Time, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	var startValue, sentValue sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT outage_started_at,alert_sent_at FROM status_alert_state WHERE id=1").Scan(&startValue, &sentValue)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, err
	}
	var outageStartedAt time.Time
	if startValue.Valid {
		outageStartedAt = time.UnixMilli(startValue.Int64)
	}
	var alertSentAt sql.NullInt64
	if sentValue.Valid {
		alertSentAt = sentValue
	}
	var event string
	eventOutageStartedAt := outageStartedAt
	nowMillis := observedAt.UnixMilli()
	if allOffline {
		if outageStartedAt.IsZero() {
			outageStartedAt = observedAt
		}
		eventOutageStartedAt = outageStartedAt
		channelsEnabled := (s.Config.StatusAlertEmailEnabled && strings.TrimSpace(s.Config.StatusAlertEmail) != "") ||
			(s.Config.StatusAlertWebhookEnabled && strings.TrimSpace(s.Config.StatusAlertWebhookURL) != "")
		if !alertSentAt.Valid && channelsEnabled && observedAt.Sub(outageStartedAt) >= s.Config.StatusAlertAfter {
			alertSentAt = sql.NullInt64{Int64: nowMillis, Valid: true}
			event = "outage"
		}
	} else if !outageStartedAt.IsZero() {
		eventOutageStartedAt = outageStartedAt
		if alertSentAt.Valid && s.Config.StatusAlertRecoveryEnabled {
			event = "recovery"
		}
		outageStartedAt = time.Time{}
		alertSentAt = sql.NullInt64{}
	}
	var startDB, sentDB any
	if !outageStartedAt.IsZero() {
		startDB = outageStartedAt.UnixMilli()
	}
	if alertSentAt.Valid {
		sentDB = alertSentAt.Int64
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO status_alert_state(id,outage_started_at,alert_sent_at,updated_at) VALUES(1,?,?,?)
		ON CONFLICT(id) DO UPDATE SET outage_started_at=excluded.outage_started_at,
		alert_sent_at=excluded.alert_sent_at,updated_at=excluded.updated_at`, startDB, sentDB, nowMillis)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", time.Time{}, err
	}
	return event, eventOutageStartedAt, nil
}

func (s *Server) dispatchStatusAlert(ctx context.Context, event string, outageStartedAt time.Time, targets []statusTarget) {
	recovered := event == "recovery"
	state := "全部线路离线"
	if recovered {
		state = "线路状态已恢复"
	}
	var detail strings.Builder
	for _, target := range targets {
		fmt.Fprintf(&detail, "\n- %s (%s)", target.Name, net.JoinHostPort(target.Host, strconv.Itoa(target.Port)))
	}
	body := fmt.Sprintf("%s\n离线开始时间：%s\n检测线路：%s", state, outageStartedAt.Format(time.RFC3339), detail.String())
	if s.Config.StatusAlertEmailEnabled && strings.TrimSpace(s.Config.StatusAlertEmail) != "" {
		if s.Config.MailSender == nil {
			log.Print("状态邮件告警发送失败：未配置 SMTP")
		} else {
			subject := "线路告警：全部线路离线"
			if recovered {
				subject = "线路恢复：服务状态正常"
			}
			sendCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
			if err := s.Config.MailSender.Send(sendCtx, s.Config.StatusAlertEmail, subject, body); err != nil {
				log.Printf("状态邮件告警发送失败: %v", err)
			}
			cancel()
		}
	}
	if s.Config.StatusAlertWebhookEnabled && strings.TrimSpace(s.Config.StatusAlertWebhookURL) != "" {
		alertTargets := make([]map[string]any, 0, len(targets))
		for _, target := range targets {
			alertTargets = append(alertTargets, map[string]any{
				"name": target.Name, "host": target.Host, "port": target.Port,
			})
		}
		payload := map[string]any{
			"event": event, "status": map[bool]string{true: "recovered", false: "offline"}[recovered],
			"outageStartedAt": outageStartedAt.UnixMilli(), "targets": alertTargets,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			log.Print("状态 Webhook 告警编码失败")
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(sendCtx, http.MethodPost, s.Config.StatusAlertWebhookURL, strings.NewReader(string(encoded)))
		if err != nil {
			log.Print("状态 Webhook 告警请求无效")
			return
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
		if err != nil {
			log.Print("状态 Webhook 告警发送失败")
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			log.Printf("状态 Webhook 告警返回 HTTP %d", response.StatusCode)
		}
	}
}

func validateStatusAlertEmail(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	address, err := mail.ParseAddress(trimmed)
	if err != nil || address.Address != trimmed {
		return "", errors.New("状态告警邮箱地址无效")
	}
	return trimmed, nil
}

func validateStatusAlertWebhook(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("状态告警 Webhook 必须是 HTTP 或 HTTPS 地址")
	}
	return trimmed, nil
}
