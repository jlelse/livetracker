package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed static
var staticFiles embed.FS

// Application name constant
const appName = "LiveTracker"

// Main application struct holding config, DB, hub, and prepared statement
type app struct {
	config             appConfig
	hub                *websocketHub
	db                 *sql.DB
	insertLocationStmt *sql.Stmt
}

// Configuration for the application, loaded from environment variables
type appConfig struct {
	port   string
	dbPath string
	token  string
	user   string
	pass   string
}

// WebSocket hub for managing clients and broadcasting messages
type websocketHub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan locationPoint
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.Mutex
}

// Struct representing a single location point
type locationPoint struct {
	Latitude  float64  `json:"lat"`
	Longitude float64  `json:"lon"`
	Timestamp int64    `json:"timestamp"`
	Altitude  *float64 `json:"altitude,omitempty"`
	Speed     *float64 `json:"speed,omitempty"`
	Bearing   *float64 `json:"bearing,omitempty"`
	Accuracy  *float64 `json:"hdop,omitempty"`
}

// Database migration struct
type migration struct {
	id  string
	sql string
}

// List of database migrations
var migrations = []migration{
	{
		id: "001_initial_schema",
		sql: `
CREATE TABLE IF NOT EXISTS locations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    latitude REAL NOT NULL,
    longitude REAL NOT NULL,
    altitude REAL,
    speed REAL,
    bearing REAL,
    accuracy_hdop REAL,
    timestamp INTEGER NOT NULL,
    received_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`,
	},
	{
		id: "002_add_index",
		sql: `
CREATE INDEX IF NOT EXISTS idx_locations_timestamp ON locations (timestamp);
`,
	},
}

func (h *websocketHub) run() {
	// Main loop for handling client registration, unregistration, and broadcasting
	for {
		select {
		case client := <-h.register:
			// Register new WebSocket client
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			log.Println("WebSocket client registered")
		case client := <-h.unregister:
			// Unregister WebSocket client
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close(websocket.StatusNormalClosure, "unregister")
				log.Println("WebSocket client unregistered")
			}
			h.mutex.Unlock()
		case message := <-h.broadcast:
			// Broadcast message to all connected clients
			h.mutex.Lock()
			msgBytes, err := json.Marshal(map[string]any{"type": "update", "payload": message})
			if err != nil {
				log.Printf("Error marshalling live update: %v", err)
				break
			}
			for client := range h.clients {
				err = client.Write(context.Background(), websocket.MessageText, msgBytes)
				if err != nil {
					log.Printf("Error writing to client: %v. Unregistering.", err)
					go func(c *websocket.Conn) {
						h.unregister <- c
					}(client)
				}
			}
			h.mutex.Unlock()
		}
	}
}

// Helper to get environment variable or fallback value
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	log.Printf("Environment variable %s not set, using default: %s", key, fallback)
	return fallback
}

func (a *app) loadConfig() {
	// Load configuration from environment variables
	a.config.port = getEnv("LIVETRACKER_PORT", "8080")
	a.config.dbPath = getEnv("LIVETRACKER_SQLITE_PATH", "tracker.db")
	a.config.token = getEnv("LIVETRACKER_API_TOKEN", "default")
	a.config.user = getEnv("LIVETRACKER_BASIC_AUTH_USER", "admin")
	a.config.pass = getEnv("LIVETRACKER_BASIC_AUTH_PASS", "admin")

	if a.config.token == "default" {
		log.Println("WARNING: LIVETRACKER_API_TOKEN is set to its default value. Please set a secure token via environment variable.")
	}
	if a.config.user == "admin" && a.config.pass == "admin" {
		log.Println("WARNING: LIVETRACKER_BASIC_AUTH_USER and/or LIVETRACKER_BASIC_AUTH_PASS are set to their default values. Please set secure credentials via environment variables.")
	}
}

func (a *app) initDB() {
	// Initialize SQLite database and apply migrations
	dbFile := a.config.dbPath
	if strings.Contains(dbFile, "?") {
		dbFile += "&"
	} else {
		dbFile += "?"
	}
	dbParams := make(url.Values)
	dbParams.Add("mode", "rwc")
	dbParams.Add("_txlock", "immediate")
	dbParams.Add("_journal_mode", "WAL")
	dbParams.Add("_busy_timeout", "1000")
	dbParams.Add("_synchronous", "NORMAL")

	var err error
	a.db, err = sql.Open("sqlite3", dbFile+dbParams.Encode())
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	if err = a.db.Ping(); err != nil {
		log.Fatalf("Error pinging database: %v", err)
	}

	log.Println("Starting database migrations...")
	_, err = a.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY);`)
	if err != nil {
		log.Fatalf("Failed to create schema_migrations table: %v", err)
	}

	appliedMigrations := make(map[string]bool)
	rows, err := a.db.Query("SELECT id FROM schema_migrations;")
	if err != nil {
		log.Fatalf("Failed to query applied migrations: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Fatalf("Failed to scan applied migration id: %v", err)
		}
		appliedMigrations[id] = true
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		log.Fatalf("Error after iterating applied migrations: %v", err)
	}

	sort.SliceStable(migrations, func(i, j int) bool {
		return migrations[i].id < migrations[j].id
	})

	for _, migration := range migrations {
		if !appliedMigrations[migration.id] {
			log.Printf("Applying migration: %s...", migration.id)
			tx, err := a.db.Begin()
			if err != nil {
				log.Fatalf("Failed to begin transaction for migration %s: %v", migration.id, err)
			}
			_, err = tx.Exec(migration.sql)
			if err != nil {
				tx.Rollback()
				log.Fatalf("Failed to execute migration %s: %v", migration.id, err)
			}
			_, err = tx.Exec("INSERT INTO schema_migrations (id) VALUES (?);", migration.id)
			if err != nil {
				tx.Rollback()
				log.Fatalf("Failed to record migration %s: %v", migration.id, err)
			}
			if err := tx.Commit(); err != nil {
				log.Fatalf("Failed to commit transaction for migration %s: %v", migration.id, err)
			}
			log.Printf("Migration %s applied successfully.", migration.id)
		} else {
			log.Printf("Migration %s already applied, skipping.", migration.id)
		}
	}
	log.Println("Database migrations finished.")
	log.Println("Database initialized successfully.")

	stmt, err := a.db.Prepare("INSERT INTO locations(latitude, longitude, altitude, speed, bearing, accuracy_hdop, timestamp) VALUES(?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		log.Fatalf("Error preparing insert statement: %v", err)
	}
	a.insertLocationStmt = stmt
}

// Helper to parse float from string or return nil
func parseFloatOrNil(s string) *float64 {
	if s == "" {
		return nil
	}
	val, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &val
}

func (a *app) trackHandler(w http.ResponseWriter, r *http.Request) {
	// Handle incoming location tracking requests
	query := r.URL.Query()

	token := query.Get("token")
	if token != a.config.token {
		http.Error(w, "Invalid API token", http.StatusUnauthorized)
		log.Printf("Unauthorized access attempt with token: %s from %s", token, r.RemoteAddr)
		return
	}

	latStr := query.Get("lat")
	lonStr := query.Get("lon")
	tsStr := query.Get("timestamp")

	if latStr == "" || lonStr == "" || tsStr == "" {
		http.Error(w, "Missing required parameters: lat, lon, timestamp", http.StatusBadRequest)
		return
	}

	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		http.Error(w, "Invalid latitude", http.StatusBadRequest)
		return
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		http.Error(w, "Invalid longitude", http.StatusBadRequest)
		return
	}
	timestamp, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid timestamp", http.StatusBadRequest)
		return
	}

	point := locationPoint{
		Latitude:  lat,
		Longitude: lon,
		Timestamp: timestamp,
		Altitude:  parseFloatOrNil(query.Get("altitude")),
		Speed:     parseFloatOrNil(query.Get("speed")),
		Bearing:   parseFloatOrNil(query.Get("bearing")),
		Accuracy:  parseFloatOrNil(query.Get("hdop")),
	}

	stmt := a.insertLocationStmt
	if stmt == nil {
		log.Printf("Insert statement not prepared")
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	_, err = stmt.Exec(point.Latitude, point.Longitude, point.Altitude, point.Speed, point.Bearing, point.Accuracy, point.Timestamp)
	if err != nil {
		log.Printf("Error saving location: %v", err)
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	log.Printf("Received location: Lat %f, Lon %f, TS %d", point.Latitude, point.Longitude, point.Timestamp)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Location received"))

	a.hub.broadcast <- point
}

// Basic authentication middleware for HTTP handlers
func (a *app) basicAuth(handler http.HandlerFunc, username, password, realm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}
}

func (a *app) wsHandler(w http.ResponseWriter, r *http.Request) {
	// Handle WebSocket upgrade and incoming messages
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading to WebSocket: %v", err)
		return
	}
	a.hub.register <- conn

	go func(c *websocket.Conn) {
		defer func() {
			a.hub.unregister <- c
		}()
		for {
			_, p, err := c.Read(context.Background())
			if err != nil {
				if websocket.CloseStatus(err) != -1 {
					log.Printf("WebSocket read error: %v", err)
				} else {
					log.Printf("WebSocket connection closed for client")
				}
				break
			}
			var msg map[string]string
			if err := json.Unmarshal(p, &msg); err == nil {
				if msgType, ok := msg["type"]; ok && msgType == "get_history" {
					a.sendHistoricalData(c)
				}
			}
		}
	}(conn)
}

func (a *app) sendHistoricalData(conn *websocket.Conn) {
	// Send historical location data (last 3 hours) to a WebSocket client
	rows, err := a.db.Query("SELECT latitude, longitude, timestamp, altitude, speed, bearing, accuracy_hdop FROM locations WHERE (timestamp / 1000) >= (unixepoch() - 10800) ORDER BY timestamp ASC")
	if err != nil {
		log.Printf("Error fetching historical data: %v", err)
		return
	}
	defer rows.Close()

	var history []locationPoint
	for rows.Next() {
		var p locationPoint
		err := rows.Scan(&p.Latitude, &p.Longitude, &p.Timestamp, &p.Altitude, &p.Speed, &p.Bearing, &p.Accuracy)
		if err != nil {
			log.Printf("Error scanning historical row: %v", err)
			continue
		}
		history = append(history, p)
	}
	if err = rows.Err(); err != nil {
		log.Printf("Error iterating historical rows: %v", err)
		return
	}

	msgBytes, err := json.Marshal(map[string]any{"type": "history", "payload": history})
	if err != nil {
		log.Printf("Error marshalling historical data: %v", err)
		return
	}

	a.hub.mutex.Lock()
	defer a.hub.mutex.Unlock()
	if _, ok := a.hub.clients[conn]; ok {
		err = conn.Write(context.Background(), websocket.MessageText, msgBytes)
		if err != nil {
			log.Printf("Error sending historical data to client: %v", err)
		} else {
			log.Printf("Sent %d historical points to client", len(history))
		}
	}
}

func main() {
	// Application entry point
	app := &app{
		hub: &websocketHub{
			clients:    make(map[*websocket.Conn]bool),
			broadcast:  make(chan locationPoint),
			register:   make(chan *websocket.Conn),
			unregister: make(chan *websocket.Conn),
		},
	}
	app.loadConfig()
	app.initDB()
	go app.hub.run()

	// Set up HTTP routes and handlers
	mux := http.NewServeMux()

	mux.HandleFunc("GET /track", app.trackHandler)
	mux.HandleFunc("GET /ws", app.basicAuth(app.wsHandler, app.config.user, app.config.pass, appName))
	staticSubFs, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /", app.basicAuth(http.FileServer(http.FS(staticSubFs)).ServeHTTP, app.config.user, app.config.pass, appName))

	srv := &http.Server{
		Addr:    ":" + app.config.port,
		Handler: mux,
	}

	// Graceful shutdown handling
	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-shutdownCh
		log.Println("Shutdown signal received, shutting down server...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		if app.insertLocationStmt != nil {
			app.insertLocationStmt.Close()
		}
		if app.db != nil {
			app.db.Close()
		}
		os.Exit(0)
	}()

	// Print startup information
	log.Printf("Server starting on port %s", app.config.port)
	log.Printf("OsmAnd URL: http://<your_ip>:%s/track?token=%s&lat={0}&lon={1}&timestamp={2}&hdop={3}&altitude={4}&speed={5}&bearing={6}", app.config.port, app.config.token)
	log.Printf("Web interface: http://<your_ip>:%s (User: %s, Pass: ***)", app.config.port, app.config.user)
	log.Printf("SQLite Path: %s", app.config.dbPath)

	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed to start: %v", err)
	}
}
