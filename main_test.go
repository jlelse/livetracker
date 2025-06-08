package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"strings"

	"github.com/coder/websocket"
	gwss "github.com/gorilla/websocket"
)

func setupTestApp(t *testing.T) *app {
	t.Helper()
	a := &app{
		hub: &websocketHub{
			clients:    make(map[*websocket.Conn]bool),
			broadcast:  make(chan locationPoint, 10),
			register:   make(chan *websocket.Conn),
			unregister: make(chan *websocket.Conn),
		},
	}
	a.config = appConfig{
		port:   "0",
		dbPath: ":memory:",
		token:  "testtoken",
		user:   "testuser",
		pass:   "testpass",
	}
	a.initDB()
	go a.hub.run()
	return a
}

func TestMigrationsAndInsert(t *testing.T) {
	// Test that migrations are applied and location insert works
	a := setupTestApp(t)
	defer a.db.Close()
	row := a.db.QueryRow("SELECT COUNT(*) FROM schema_migrations;")
	var count int
	if err := row.Scan(&count); err != nil || count == 0 {
		t.Fatalf("Migrations not applied: %v, count=%d", err, count)
	}
	_, err := a.insertLocationStmt.Exec(1.1, 2.2, nil, nil, nil, nil, 1234567890)
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	row = a.db.QueryRow("SELECT latitude, longitude, timestamp FROM locations WHERE latitude=1.1 AND longitude=2.2;")
	var lat, lon float64
	var ts int64
	if err := row.Scan(&lat, &lon, &ts); err != nil {
		t.Fatalf("Row not found: %v", err)
	}
	if lat != 1.1 || lon != 2.2 || ts != 1234567890 {
		t.Fatalf("Unexpected values: %v %v %v", lat, lon, ts)
	}
}

func TestTrackHandler_Success(t *testing.T) {
	// Test that /track endpoint inserts a location with all parameters
	a := setupTestApp(t)
	defer a.db.Close()
	ts := httptest.NewServer(http.HandlerFunc(a.trackHandler))
	defer ts.Close()
	params := url.Values{
		"token":     {a.config.token},
		"lat":       {"50.1"},
		"lon":       {"8.6"},
		"timestamp": {"1680000000"},
		"hdop":      {"1.2"},
		"altitude":  {"100.5"},
		"speed":     {"10.0"},
		"bearing":   {"180.0"},
	}
	resp, err := http.Get(ts.URL + "/track?" + params.Encode())
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d", resp.StatusCode)
	}
	row := a.db.QueryRow("SELECT latitude, longitude, timestamp, accuracy_hdop, altitude, speed, bearing FROM locations WHERE latitude=50.1 AND longitude=8.6;")
	var lat, lon, hdop, alt, speed, bearing sql.NullFloat64
	var tsInt int64
	if err := row.Scan(&lat, &lon, &tsInt, &hdop, &alt, &speed, &bearing); err != nil {
		t.Fatalf("Row not found: %v", err)
	}
	if !lat.Valid || lat.Float64 != 50.1 || !lon.Valid || lon.Float64 != 8.6 {
		t.Fatalf("Unexpected lat/lon: %v %v", lat, lon)
	}
}

func TestTrackHandler_InvalidToken(t *testing.T) {
	// Test that /track endpoint returns 401 for invalid token
	a := setupTestApp(t)
	defer a.db.Close()
	ts := httptest.NewServer(http.HandlerFunc(a.trackHandler))
	defer ts.Close()
	params := url.Values{
		"token":     {"wrong"},
		"lat":       {"50.1"},
		"lon":       {"8.6"},
		"timestamp": {"1680000000"},
	}
	resp, err := http.Get(ts.URL + "/track?" + params.Encode())
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Expected 401, got %d", resp.StatusCode)
	}
}

func TestTrackHandler_MissingParams(t *testing.T) {
	// Test that /track endpoint returns 400 for missing parameters
	a := setupTestApp(t)
	defer a.db.Close()
	ts := httptest.NewServer(http.HandlerFunc(a.trackHandler))
	defer ts.Close()
	params := url.Values{
		"token": {a.config.token},
		"lat":   {"50.1"},
	}
	resp, err := http.Get(ts.URL + "/track?" + params.Encode())
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d", resp.StatusCode)
	}
}

func TestBasicAuth(t *testing.T) {
	// Test that basic authentication works as expected
	a := setupTestApp(t)
	h := a.basicAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}, a.config.user, a.config.pass, "testrealm")
	ts := httptest.NewServer(http.HandlerFunc(h))
	defer ts.Close()
	resp, _ := http.Get(ts.URL)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Expected 401, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL, nil)
	req.SetBasicAuth("foo", "bar")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Expected 401, got %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", ts.URL, nil)
	req.SetBasicAuth(a.config.user, a.config.pass)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d", resp.StatusCode)
	}
}

func TestParseFloatOrNil(t *testing.T) {
	// Test that parseFloatOrNil returns correct values for various inputs
	if parseFloatOrNil("") != nil {
		t.Fatal("Expected nil for empty string")
	}
	if v := parseFloatOrNil("abc"); v != nil {
		t.Fatal("Expected nil for invalid float")
	}
	if v := parseFloatOrNil("1.23"); v == nil || *v != 1.23 {
		t.Fatalf("Expected 1.23, got %v", v)
	}
}

func TestSendHistoricalData(t *testing.T) {
	// Test that the WebSocket handler sends historical location data on get_history request
	a := setupTestApp(t)
	defer a.db.Close()

	// Insert a location with a recent timestamp
	now := time.Now().Unix() * 1000
	_, err := a.insertLocationStmt.Exec(10.0, 20.0, nil, nil, nil, nil, now)
	if err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Start an HTTP server with wsHandler (without BasicAuth)
	ts := httptest.NewServer(http.HandlerFunc(a.wsHandler))
	defer ts.Close()

	// Build the WebSocket URL
	u := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// Build a WebSocket client (without Auth)
	dialer := gwss.DefaultDialer
	c, resp, err := dialer.Dial(u, nil)
	if err != nil {
		body := ""
		if resp != nil {
			b := make([]byte, 1024)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		t.Fatalf("WebSocket dial failed: %v, body: %s", err, body)
	}
	defer c.Close()

	// Wait to ensure registration in the hub is complete
	time.Sleep(100 * time.Millisecond)

	// Send get_history request
	msg := map[string]string{"type": "get_history"}
	if err := c.WriteJSON(msg); err != nil {
		t.Fatalf("WriteJSON failed: %v", err)
	}

	// Read response
	type wsMsg struct {
		Type    string          `json:"type"`
		Payload []locationPoint `json:"payload"`
	}
	var reply wsMsg
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := c.ReadJSON(&reply); err != nil {
		t.Fatalf("ReadJSON failed: %v", err)
	}
	if reply.Type != "history" {
		t.Fatalf("Expected type=history, got %s", reply.Type)
	}
	if len(reply.Payload) == 0 {
		t.Fatalf("Expected at least one location in history")
	}
	found := false
	for _, p := range reply.Payload {
		if p.Latitude == 10.0 && p.Longitude == 20.0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("Inserted location not found in history payload")
	}
}
