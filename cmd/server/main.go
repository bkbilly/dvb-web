package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"dvb-web/internal/api"
	"dvb-web/internal/db"
	"dvb-web/internal/epg"
	"dvb-web/internal/transcoder"
	"dvb-web/internal/tuner"
	"dvb-web/internal/webrtc"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server port")
	dataDir := flag.String("data-dir", "./data", "Path to data directory for database and channel logos")
	webDir := flag.String("web-dir", "./web", "Path to web frontend assets directory")
	mock := flag.Bool("mock", false, "Force mock/virtual tuner for testing without physical DVB hardware")
	flag.Parse()

	log.Println("==================================================")
	log.Println("          DVB-Web TV Streaming Server             ")
	log.Println("==================================================")

	// 1. Initialize SQLite Database
	database, err := db.Open(*dataDir)
	if err != nil {
		log.Fatalf("Fatal: Failed to initialize database: %v", err)
	}
	defer database.Close()
	log.Printf("[DB] Initialized database at %s/tv.db", *dataDir)

	// 2. Hardware Transcoder Detection
	detector := transcoder.NewDetector()
	log.Printf("[Transcoder] Acceleration mode: %s", detector.GetActiveHardware())

	// 3. EPG Parser & Broadcast Wire
	var server *api.Server
	var lastEPGBroadcast time.Time
	var epgMu sync.Mutex
	epgParser := epg.NewParser(database, func(events []db.EPGEvent) {
		if server != nil {
			epgMu.Lock()
			now := time.Now()
			if now.Sub(lastEPGBroadcast) >= 2*time.Minute {
				lastEPGBroadcast = now
				epgMu.Unlock()
				server.BroadcastEPGUpdate(events)
			} else {
				epgMu.Unlock()
			}
		}
	})

	// 4. Tuner Pool
	pool := tuner.NewPool(database, detector, epgParser, *mock)

	// 5. WebRTC Session Manager
	rtcManager := webrtc.NewManager()

	// 6. API Server
	server = api.NewServer(database, pool, detector, rtcManager, epgParser)

	// Only seed mock channels and EPG if mock mode was explicitly requested
	if *mock {
		channels, _ := database.ListChannels()
		if len(channels) == 0 {
			log.Println("[Init] Mock mode active: Seeding simulated channels & EPG...")
			seedInitialChannels(database)
			channels, _ = database.ListChannels()
			epg.SeedMockEPG(database, channels)
		}
	}

	// 7. HTTP Routes
	mux := http.NewServeMux()
	server.RegisterRoutes(mux, *webDir)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	// Graceful shutdown handling (SIGINT, SIGTERM, SIGHUP)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		log.Printf("[Server] Web application listening on http://0.0.0.0:%d", *port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Fatal: HTTP server failed: %v", err)
		}
	}()

	<-stop
	log.Println("[Server] Shutdown signal received. Closing server and releasing hardware...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(shutdownCtx)
	cancel()

	rtcManager.CloseAll()
	pool.StopAll()
	epgParser.Stop()
	log.Println("[Server] Stopped cleanly. Goodbye!")
	os.Exit(0)
}

func seedInitialChannels(database *db.Database) {
	initial := []db.Channel{
		{
			Name:           "BBC One HD",
			Number:         1,
			Frequency:      506000000,
			Bandwidth:      8000000,
			DeliverySystem: "DVBT2",
			ServiceID:      101,
			VideoPID:       1011,
			AudioPID:       1012,
			Enabled:        true,
		},
		{
			Name:           "BBC Two HD",
			Number:         2,
			Frequency:      506000000,
			Bandwidth:      8000000,
			DeliverySystem: "DVBT2",
			ServiceID:      102,
			VideoPID:       1021,
			AudioPID:       1022,
			Enabled:        true,
		},
		{
			Name:           "ITV1 HD",
			Number:         3,
			Frequency:      538000000,
			Bandwidth:      8000000,
			DeliverySystem: "DVBT2",
			ServiceID:      201,
			VideoPID:       2011,
			AudioPID:       2012,
			Enabled:        true,
		},
		{
			Name:           "Channel 4 HD",
			Number:         4,
			Frequency:      538000000,
			Bandwidth:      8000000,
			DeliverySystem: "DVBT2",
			ServiceID:      202,
			VideoPID:       2021,
			AudioPID:       2022,
			Enabled:        true,
		},
		{
			Name:           "Sky News",
			Number:         11,
			Frequency:      570000000,
			Bandwidth:      8000000,
			DeliverySystem: "DVBT",
			ServiceID:      301,
			VideoPID:       3011,
			AudioPID:       3012,
			Enabled:        true,
		},
	}

	for _, c := range initial {
		_ = database.SaveChannel(&c)
	}
}
