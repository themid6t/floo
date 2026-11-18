package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/themid6t/floo/internal/broker"
)

type Server struct {
	Broker *broker.Broker
	Token  string
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, http.StatusOK, map[string]bool{"ok": true}) })
	m.HandleFunc("POST /v1/sessions", s.register)
	m.HandleFunc("GET /v1/peers", s.peers)
	m.HandleFunc("POST /v1/messages", s.send)
	m.HandleFunc("GET /v1/messages", s.poll)
	m.HandleFunc("POST /v1/acks", s.ack)
	m.HandleFunc("GET /v1/history", s.history)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Require a non-simple header to prevent a web page from sending
		// cross-origin requests to a local, unauthenticated broker.
		if r.Header.Get("X-Floo-Client") != "1" {
			jsonReply(w, http.StatusForbidden, map[string]string{"error": "missing Floo client header"})
			return
		}
		if s.Token != "" && r.Header.Get("Authorization") != "Bearer "+s.Token {
			jsonReply(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		m.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func jsonReply(w http.ResponseWriter, code int, x any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(x)
}

func bad(w http.ResponseWriter, err error) {
	jsonReply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var v broker.Session
	if err := decode(w, r, &v); err != nil {
		bad(w, err)
		return
	}
	if err := s.Broker.Register(v); err != nil {
		bad(w, err)
		return
	}
	jsonReply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) peers(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, http.StatusOK, s.Broker.Peers(r.URL.Query().Get("project")))
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var v struct{ From, To, Text string }
	if err := decode(w, r, &v); err != nil {
		bad(w, err)
		return
	}
	seq, err := s.Broker.Send(v.From, v.To, v.Text)
	if err != nil {
		bad(w, err)
		return
	}
	jsonReply(w, http.StatusAccepted, map[string]int64{"seq": seq})
}

func (s *Server) poll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, err := strconv.ParseInt(q.Get("after"), 10, 64)
	if err != nil || after < 0 {
		bad(w, errors.New("invalid after"))
		return
	}
	wait, err := strconv.Atoi(q.Get("wait_ms"))
	if err != nil || wait < 0 || wait > 30000 {
		bad(w, errors.New("wait_ms must be 0..30000"))
		return
	}
	msgs, ch, err := s.Broker.Poll(q.Get("session"), after)
	if err != nil {
		bad(w, err)
		return
	}
	if len(msgs) == 0 && wait > 0 {
		t := time.NewTimer(time.Duration(wait) * time.Millisecond)
		defer t.Stop()
		select {
		case <-ch:
		case <-t.C:
		case <-r.Context().Done():
			return
		}
		msgs, _, err = s.Broker.Poll(q.Get("session"), after)
		if err != nil {
			bad(w, err)
			return
		}
	}
	jsonReply(w, http.StatusOK, msgs)
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Session string `json:"session"`
		Seq     int64  `json:"seq"`
	}
	if err := decode(w, r, &v); err != nil {
		bad(w, err)
		return
	}
	if err := s.Broker.Ack(v.Session, v.Seq); err != nil {
		bad(w, err)
		return
	}
	jsonReply(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	jsonReply(w, http.StatusOK, s.Broker.History(r.URL.Query().Get("session")))
}
