package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCouponListReleasesSingleConnectionBeforeNestedQueries(t *testing.T) {
	server, db := testServer(t)
	_, err := db.Exec(`
		INSERT INTO coupons(code,type,expires_at,one_time_use,used,created_at,updated_at)
		VALUES('REGRESSION','item','2099-01-01T00:00:00Z',1,1,'2025-01-01T00:00:00Z',1);
		INSERT INTO coupon_items(coupon_code,position,item_id,amount) VALUES('REGRESSION',0,'stone',2);
		INSERT INTO coupon_usages(coupon_code,username_key,username,used_at) VALUES('REGRESSION','player','Player',1);`)
	if err != nil {
		t.Fatal(err)
	}
	server.adminSessions.Put("admin-regression", "admin", time.Now().Add(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api?method=getCoupons&session=admin-regression", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var body struct {
		Success bool `json:"success"`
		Coupons []struct {
			Items []struct {
				ItemID string `json:"itemId"`
				Amount int    `json:"amount"`
			} `json:"items"`
			UsedBy []string `json:"usedBy"`
		} `json:"coupons"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success || len(body.Coupons) != 1 || len(body.Coupons[0].Items) != 1 || body.Coupons[0].Items[0].Amount != 2 || body.Coupons[0].Items[0].ItemID != "stone" || len(body.Coupons[0].UsedBy) != 1 || body.Coupons[0].UsedBy[0] != "Player" {
		t.Fatalf("单连接查询未完整返回兑换物品和使用记录：%s", response.Body.String())
	}
}

func TestAuthorizationCanExtendAndCloseBeforeJoin(t *testing.T) {
	server, db := testServer(t)
	_, err := db.Exec(`INSERT INTO users(id,name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at)
		VALUES(1,'Player','player','fixture','fixture','p@example.test','p@example.test','active','2025-01-01',1234,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	server.sessions.Put("web-regression", "Player", time.Now().Add(time.Hour))
	for _, check := range []struct{ method, message string }{
		{"login", "玩家名称Player已确认登录"},
		{"login", "你的再次登录已确认"},
		{"logout", "玩家Player已退出登录！"},
		{"logout", "玩家Player现在不是登录状态！"},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api?method="+check.method+"&name=Player&session=web-regression&time=600000", nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), check.message) {
			t.Fatalf("%s 返回不兼容：%d %s", check.method, response.Code, response.Body.String())
		}
	}
	var onlineTime int64
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE id=1").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != 1234 {
		t.Fatalf("仅网页授权和退出不得累计游戏在线时长：%d", onlineTime)
	}
}
