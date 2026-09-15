package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"

	"github.com/ildus/zigbeemq/internal/hub"
)

//go:embed static
var staticRoot embed.FS

type Server struct {
	hub *hub.Hub
	mux *http.ServeMux
}

func New(h *hub.Hub) *Server {
	s := &Server{hub: h, mux: http.NewServeMux()}
	static, err := fs.Sub(staticRoot, "static")
	if err != nil {
		panic(err)
	}
	s.mux.HandleFunc("GET /api/network", s.network)
	s.mux.HandleFunc("GET /api/devices", s.devices)
	s.mux.HandleFunc("POST /api/devices/{ieee}/on", s.on)
	s.mux.HandleFunc("POST /api/devices/{ieee}/off", s.off)
	s.mux.HandleFunc("POST /api/devices/{ieee}/brightness", s.brightness)
	s.mux.HandleFunc("POST /api/devices/{ieee}/interview", s.interview)
	s.mux.HandleFunc("PATCH /api/devices/{ieee}", s.update)
	s.mux.HandleFunc("DELETE /api/devices/{ieee}", s.remove)
	s.mux.HandleFunc("POST /api/permit-join", s.permitJoin)
	s.mux.HandleFunc("GET /api/events", s.events)
	s.mux.HandleFunc("POST /api/refresh", s.refresh)
	s.mux.Handle("/", http.FileServer(http.FS(static)))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	info, err := s.hub.Network(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ieee":     hub.IEEE(info.IEEEAddr),
		"nwk_addr": info.ShortAddr,
		"pan_id":   info.PanID,
		"channel":  info.Channel,
		"assoc":    info.NumAssocDevices,
	})
}

func (s *Server) devices(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": s.hub.Devices(),
		"permit_until": func() any {
			t := s.hub.PermitUntil()
			if t.IsZero() {
				return nil
			}
			return t
		}(),
	})
}

func (s *Server) on(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Turn(r.Context(), r.PathValue("ieee"), true); err != nil {
		status := http.StatusBadGateway
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "on"})
}

func (s *Server) off(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Turn(r.Context(), r.PathValue("ieee"), false); err != nil {
		status := http.StatusBadGateway
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "off"})
}

func (s *Server) brightness(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Percent uint8 `json:"percent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.hub.SetBrightness(r.Context(), r.PathValue("ieee"), body.Percent); err != nil {
		status := http.StatusBadGateway
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": "brightness", "percent": body.Percent})
}

func (s *Server) interview(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Interview(r.Context(), r.PathValue("ieee")); err != nil {
		status := http.StatusBadGateway
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	d, _ := s.hub.Device(r.PathValue("ieee"))
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string   `json:"name"`
		Kind hub.Kind `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.hub.Update(r.PathValue("ieee"), body.Name, body.Kind); err != nil {
		status := http.StatusBadRequest
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	d, _ := s.hub.Device(r.PathValue("ieee"))
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Remove(r.Context(), r.PathValue("ieee")); err != nil {
		status := http.StatusBadGateway
		if hub.IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "removed"})
}

func (s *Server) permitJoin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Seconds uint8 `json:"seconds"`
	}
	body.Seconds = 60
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.hub.PermitJoin(r.Context(), body.Seconds); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seconds": body.Seconds})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if err := s.hub.Refresh(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": s.hub.Devices()})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, unsub := s.hub.Subscribe()
	defer unsub()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			payload, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(payload)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}
