package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/radovskyb/watcher"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

type Path string

type Worker struct {
	dir Path
	running bool
}

func (worker *Worker) listen(ctx context.Context, w *watcher.Watcher) {
	defer func() {
		log.Printf("Worker for %v shut down", worker.dir)
		worker.running = false
	}()
	// Seems dumb - must be another way
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	worker.running = true

	for {
		select {
		case event := <-w.Event:
			fmt.Println(event)
			timer.Reset(time.Second * 5)
		case err := <-w.Error:
			log.Printf("Watcher died: %s", err)
			w.Close()
			return
		case <-timer.C:
			log.Printf("Working on commit\n")
		case <-ctx.Done():
			log.Printf("Shutting down %v watcher", worker.dir)
			w.Close()
			return
		case <-w.Closed:
			return
		}
	}
}

func (worker *Worker) Start(ctx context.Context) error {
	if _, err := os.Stat(string(worker.dir)); os.IsNotExist(err) {
		return errors.New(fmt.Sprintf("Directory %s was not found", worker.dir))
	}
	w := watcher.New()
	context, cancel := context.WithCancel(ctx)
	go worker.listen(context, w)
	if err := w.AddRecursive(string(worker.dir)); err != nil {
		log.Fatalln(err)
	}
	go func() {
		defer func() {
			cancel()
		}()
		if err := w.Start(time.Second); err != nil {
			log.Printf("Watcher died: %s", err)
		}
	}()
	return nil
}

func (worker *Worker) IsRunning() bool {
	return worker.running
}

func GetWorker(path Path) *Worker {
	return &Worker{
		dir:           path,
		running: false,
	}
}

type Server struct {
	port uint16
	running bool
	workers []*Worker
}

type registerRequest struct {
	Dir Path `json:"dir"`
}


func (srv *Server) registerWorkerHandler(ctx context.Context) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		decoder := json.NewDecoder(r.Body)
		var body registerRequest
		err := decoder.Decode(&body)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusUnprocessableEntity), http.StatusUnprocessableEntity)
			return
		}
		log.Printf("Registering worker for %s", body.Dir)
		worker := GetWorker(body.Dir)
		err = worker.Start(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		srv.workers = append(srv.workers, worker)
	}
}


func (srv *Server) Handle(ctx context.Context) {
	r := mux.NewRouter()
	r.HandleFunc("/register", srv.registerWorkerHandler(ctx)).Methods("POST")
	server := &http.Server{
		Addr: fmt.Sprintf(":%d", srv.port),
		Handler: r,
	}
	srv.running = true
	go func() {
		go func() {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("%s", err)
			}
			srv.running = false
		}()
		<-ctx.Done()
		err := server.Shutdown(context.Background())
		if err != nil {
			log.Fatalln(err)
		}
	}()
}

func (srv *Server) IsRunning() bool {
	workersAreRunning := false
	for _, worker := range srv.workers {
		workersAreRunning = workersAreRunning || worker.IsRunning()
	}
	return srv.running || workersAreRunning
}

func main() {
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigchan
		log.Printf("Shutting down watcher")
		cancel()
	}()
	worker := GetWorker("/Users/kristoncosta/Workspace/tester")
	workers := []*Worker{worker}
	srv := Server{
		port: 8001,
		running: false,
		workers: workers,
	}

	worker.Start(ctx)
	srv.Handle(ctx)
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second * 10)
	defer func() {
		cancel()
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !srv.IsRunning() {
			log.Printf("Watcher has gracefully shut-down")
			return
		}
		select {
		case <-shutdownCtx.Done():
			log.Fatalf("Timed out trying to shut down watcher")
			return
		case <-ticker.C:
		}
	}
}
