package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gbcserver/source/internal/imageutil"
)

func TestUploadItemImageMultipartAndLocalReferenceOnly(t *testing.T) {
	server, db := testServer(t)
	server.staticDir = t.TempDir()
	server.adminSessions.Put("admin-upload", "admin", time.Now().Add(time.Hour))

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "item.png")
	if err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	sample := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			sample.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: uint8((x * y) % 255), A: 255})
		}
	}
	if err := png.Encode(&imageData, sample); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(imageData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/uploadItemImage", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("X-Admin-Session", "admin-upload")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("multipart 上传失败: %d %s", response.Code, response.Body.String())
	}
	var uploaded struct {
		Success  bool   `json:"success"`
		ImageURL string `json:"imageUrl"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if !uploaded.Success || !strings.HasPrefix(uploaded.ImageURL, "/items/") {
		t.Fatalf("上传响应不是本地商品图片: %s", response.Body.String())
	}
	imagePath := filepath.Join(server.staticDir, filepath.FromSlash(strings.TrimPrefix(uploaded.ImageURL, "/")))
	if _, err := os.Stat(imagePath); err != nil {
		t.Fatalf("上传文件未落盘: %v", err)
	}
	compressed, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	width, height, err := imageutil.DecodeDimensions(compressed)
	if err != nil || width > 512 || height > 256 || width*2 != height*4 {
		t.Fatalf("商品图片上传后未自动缩放: %dx%d bytes=%d original=%d err=%v", width, height, len(compressed), imageData.Len(), err)
	}

	for _, image := range []string{"https://example.invalid/items/remote.png", "data:image/png;base64,AAAA"} {
		payload := `{"name":"外链测试","itemId":"minecraft:stone","points":1,"image":"` + image + `"}`
		request := httptest.NewRequest(http.MethodPost, "/api?method=addShopItem", strings.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", "admin-upload")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"success":true`) {
			t.Fatalf("外部商品图片未被拒绝: %s", response.Body.String())
		}
	}

	payload := `{"name":"本地图片","itemId":"minecraft:stone","points":1,"image":"` + uploaded.ImageURL + `"}`
	request = httptest.NewRequest(http.MethodPost, "/api?method=addShopItem", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Admin-Session", "admin-upload")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) {
		t.Fatalf("上传后的本地图片不能保存: %d %s", response.Code, response.Body.String())
	}
	_ = db
}

func TestUploadItemImageRejectsWebPAndGIF(t *testing.T) {
	server, _ := testServer(t)
	server.staticDir = t.TempDir()
	server.adminSessions.Put("admin-upload", "admin", time.Now().Add(time.Hour))
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "item.webp", data: []byte("RIFF0000WEBPVP8 ")},
		{name: "item.gif", data: []byte("GIF89a")},
	} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", test.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(test.data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/uploadItemImage", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		request.Header.Set("X-Admin-Session", "admin-upload")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), `"success":true`) {
			t.Fatalf("%s 应被拒绝: %d %s", test.name, response.Code, response.Body.String())
		}
	}
}

func TestUploadItemImageAcceptsJPEG(t *testing.T) {
	server, _ := testServer(t)
	server.staticDir = t.TempDir()
	server.adminSessions.Put("admin-upload", "admin", time.Now().Add(time.Hour))
	var imageData bytes.Buffer
	sample := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			sample.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}
	if err := jpeg.Encode(&imageData, sample, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "item.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(imageData.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/uploadItemImage", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("X-Admin-Session", "admin-upload")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) {
		t.Fatalf("JPEG 商品图片应允许上传: %d %s", response.Code, response.Body.String())
	}
}

func TestUploadAvatarAutomaticallyResizesPNG(t *testing.T) {
	server, db := testServer(t)
	server.avatarDir = t.TempDir()
	if _, err := db.Exec(`INSERT INTO users(name,name_key,uuid,password_hash,email,email_key,status,join_date,created_at,updated_at)
		VALUES('玩家甲','玩家甲','123e4567-e89b-12d3-a456-426614174011','hash','avatar@example.invalid','avatar@example.invalid','active','2026-01-01',1,1)`); err != nil {
		t.Fatal(err)
	}
	server.sessions.Put("avatar-session", "玩家甲", time.Now().Add(time.Hour))
	sample := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			sample.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: uint8((x * y) % 255), A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, sample); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"username": "玩家甲",
		"avatar":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(encoded.Bytes()),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/uploadAvatar", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Web-Session", "avatar-session")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("头像上传失败: %d %s", response.Code, response.Body.String())
	}
	var uploaded struct {
		AvatarURL string `json:"avatarUrl"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(uploaded.AvatarURL, "/avatars/")
	compressed, err := os.ReadFile(filepath.Join(server.avatarDir, name))
	if err != nil {
		t.Fatal(err)
	}
	width, height, err := imageutil.DecodeDimensions(compressed)
	if err != nil || width > 256 || height > 128 || width*2 != height*4 {
		t.Fatalf("头像上传后未自动缩放: %dx%d err=%v", width, height, err)
	}
}

func TestRepairLegacyShopImageReference(t *testing.T) {
	server, db := testServer(t)
	if _, err := db.Exec(`INSERT INTO shop_items(id,name,item_id,amount,points,stock,image,description,created_at,updated_at)
        VALUES('legacy-image','旧商品','minecraft:stone',1,1,NULL,'https://gbc.242774835.xyz/items/gbcpcb.png','旧图','2025-01-01',NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := repairLegacyShopImages(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var image string
	if err := db.QueryRow("SELECT image FROM shop_items WHERE id='legacy-image'").Scan(&image); err != nil {
		t.Fatal(err)
	}
	if image != "/items/gbcpcb.png" {
		t.Fatalf("旧商品图片未转换为本地路径: %q", image)
	}
	_ = server
}
