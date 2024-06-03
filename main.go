package main

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	timeout        = 2 * time.Second
	updateInterval = 5 * time.Second
)

var (
	sentinelAddrs      = strings.Split(os.Getenv("SENTINEL_ADDRS"), ",")
	sentinelPassword   = os.Getenv("SENTINEL_PASSWORD")
	masterName         = os.Getenv("MASTER_NAME")
	masterLock         sync.RWMutex
	activeConnections  = make(map[net.Conn]struct{})
	connLock           sync.Mutex
	ctx                = context.Background()
	lastGetMasterError error
	watcher            *redis.SentinelWatcher
	masterSwitchCount  = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_proxy_master_switch_count",
			Help: "Number of master switch events",
		},
	)
	getMasterFailureCount = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_proxy_get_master_failure_count",
			Help: "Number of master switch events",
		},
	)
	openConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "redis_proxy_open_connections",
			Help: "Current number of open connections",
		},
	)
	totalConnections = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "redis_proxy_total_connections",
			Help: "Total number of connections received",
		},
	)
)

func init() {
	prometheus.MustRegister(masterSwitchCount)
	prometheus.MustRegister(getMasterFailureCount)
	prometheus.MustRegister(openConnections)
	prometheus.MustRegister(totalConnections)
}

func updateMasterAddr() {
	for {
		_, lastGetMasterError = watcher.MasterAddr(ctx)
		if lastGetMasterError != nil {
			getMasterFailureCount.Inc()
			log.Printf("Error getting master address: %v", lastGetMasterError)
		}
		time.Sleep(updateInterval)
	}
}

//goland:noinspection GoUnhandledErrorResult
func handleClient(conn net.Conn) {
	defer conn.Close()
	totalConnections.Inc()
	masterLock.RLock()

	master := watcher.CurrentMaster
	connLock.Lock()
	activeConnections[conn] = struct{}{}
	connLock.Unlock()

	defer func() {
		connLock.Lock()
		delete(activeConnections, conn)
		openConnections.Dec()
		connLock.Unlock()
	}()

	openConnections.Inc()
	masterLock.RUnlock()

	masterConn, err := net.DialTimeout("tcp", master, timeout)
	if err != nil {
		log.Printf("Error connecting to master: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		io.Copy(masterConn, conn)
		masterConn.Close()
	}()
	go func() {
		defer wg.Done()
		io.Copy(conn, masterConn)
		conn.Close()
	}()
	wg.Wait()
}

//goland:noinspection GoUnhandledErrorResult
func startProxy() {
	listener, err := net.Listen("tcp", ":6379")
	if err != nil {
		log.Fatalf("Error starting TCP listener: %v", err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %v", err)
			continue
		}
		go handleClient(conn)
	}
}

//goland:noinspection GoUnhandledErrorResult
func closeOldConnections() {
	connLock.Lock()
	defer connLock.Unlock()
	for conn := range activeConnections {
		conn.Close()
	}
	activeConnections = make(map[net.Conn]struct{})
	openConnections.Set(0)
}

//goland:noinspection GoUnhandledErrorResult
func startHTTPServer() {
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		masterLock.RLock()
		defer masterLock.RUnlock()
		if lastGetMasterError != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("no master"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func main() {
	if len(sentinelAddrs) == 0 {
		log.Fatalf("Please set SENTINEL_ADDRS env-var")
	}
	if masterName == "" {
		log.Fatalf("Please set MASTER_NAME env-var")
	}
	watcher = &redis.SentinelWatcher{
		OnMaster: func(ctx context.Context, addr string) {
			masterSwitchCount.Inc()
			closeOldConnections()
		},
	}
	err := watcher.Initialize(ctx, &redis.FailoverOptions{
		MasterName:       masterName,
		SentinelAddrs:    sentinelAddrs,
		SentinelPassword: sentinelPassword,
	})
	if err != nil {
		log.Fatalf("Failed to get initial master address: %v", err)
	}
	go updateMasterAddr()
	go startHTTPServer()
	startProxy()
}
