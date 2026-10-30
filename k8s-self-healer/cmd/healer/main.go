// Command healer runs the self-healing agent, dashboard and metrics endpoint.
package main

import (
	"context"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"k8s-self-healer/internal/controller" // must match the module line in go.mod
)

//go:embed templates/index.html
var tplFS embed.FS

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", filepath.Join(os.Getenv("HOME"), ".kube", "config"))
}

func main() {
	restCfg, err := loadConfig()
	if err != nil {
		log.Fatalf("kubeconfig error: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		log.Fatalf("API client error: %v", err)
	}

	ns := map[string]bool{}
	for _, n := range strings.Split(env("WATCH_NAMESPACES", "default"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			ns[n] = true
		}
	}
	maxHeals, _ := strconv.Atoi(env("MAX_HEALS", "5"))
	window, err := time.ParseDuration(env("HEAL_WINDOW", "10m"))
	if err != nil {
		window = 10 * time.Minute
	}
	healer := controller.New(cs, controller.Config{
		Namespaces: ns,
		DryRun:     env("DRY_RUN", "false") == "true",
		MaxHeals:   maxHeals,
		Window:     window,
	})

	tpl := template.Must(template.ParseFS(tplFS, "templates/index.html"))
	start := time.Now()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := struct {
			Uptime string
			Logs   []controller.Entry
		}{time.Since(start).Round(time.Second).String(), healer.History()}
		if err := tpl.Execute(w, data); err != nil {
			log.Printf("template error: %v", err)
		}
	})
	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Println("Starting Self-Healing Agent server on :8080...")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()
	go func() {
		if err := healer.Run(ctx); err != nil {
			log.Fatalf("healer error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}
