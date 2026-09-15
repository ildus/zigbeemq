package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ildus/zigbeemq/internal/hub"
	"github.com/ildus/zigbeemq/internal/mq"
	"github.com/ildus/zigbeemq/internal/web"
)

func main() {
	port := flag.String("port", env("GOZNP_PORT", "/dev/ttyUSB0"), "serial port of the CC2652 dongle")
	listen := flag.String("listen", env("ZIGBEEMQ_LISTEN", ":8080"), "HTTP listen address")
	dataDir := flag.String("data", env("ZIGBEEMQ_DATA", "./data"), "state directory")
	mqttBroker := flag.String("mqtt", env("ZIGBEEMQ_MQTT", "tcp://bb:1883"), "MQTT broker (empty or - to disable)")
	mqttBase := flag.String("mqtt-base", env("ZIGBEEMQ_MQTT_BASE", "zigbee2mqtt"), "MQTT base topic")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h := hub.New(log, *dataDir)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("opening adapter", "port", *port)
	if err := h.Open(ctx, *port); err != nil {
		log.Error("adapter", "err", err)
		os.Exit(1)
	}
	defer h.Close()

	if *mqttBroker != "" && *mqttBroker != "-" {
		bridge := mq.New(log, h, mq.Config{Broker: *mqttBroker, Base: *mqttBase})
		go func() {
			if err := bridge.Run(ctx); err != nil && ctx.Err() == nil {
				log.Error("mqtt", "err", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           web.New(h).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("http", "addr", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shut)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
